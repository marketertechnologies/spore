package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// PruneSettings removes from a Claude Code settings blob every hook
// entry and permissions.deny rule the managed set already provides.
// Unrelated keys, hooks and rules are kept. Groups, events, hooks,
// deny and permissions left empty by the prune are dropped. When
// nothing matches, the input comes back byte for byte and removed is
// empty. Output keys are sorted.
func PruneSettings(raw []byte, m ManagedSet) (out []byte, removed []string, err error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, nil, fmt.Errorf("parse settings: %w", err)
	}

	if rawHooks, ok := top["hooks"]; ok {
		pruned, gone, err := pruneHooks(rawHooks, m)
		if err != nil {
			return nil, nil, err
		}
		if len(gone) > 0 {
			removed = append(removed, gone...)
			setOrDrop(top, "hooks", pruned)
		}
	}

	if rawPerms, ok := top["permissions"]; ok {
		pruned, gone, err := prunePermissions(rawPerms, m)
		if err != nil {
			return nil, nil, err
		}
		if len(gone) > 0 {
			removed = append(removed, gone...)
			setOrDrop(top, "permissions", pruned)
		}
	}

	if len(removed) == 0 {
		return raw, nil, nil
	}
	out, err = encodeIndented(top)
	return out, removed, err
}

func pruneHooks(raw json.RawMessage, m ManagedSet) (map[string]json.RawMessage, []string, error) {
	var events map[string][]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, nil, fmt.Errorf("parse hooks: %w", err)
	}
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)

	var removed []string
	out := map[string]json.RawMessage{}
	for _, event := range names {
		var keptGroups []map[string]json.RawMessage
		for _, group := range events[event] {
			var entries []map[string]json.RawMessage
			if rawEntries, ok := group["hooks"]; ok {
				if err := json.Unmarshal(rawEntries, &entries); err != nil {
					return nil, nil, fmt.Errorf("parse %s hooks: %w", event, err)
				}
			}
			var matcher string
			if raw, ok := group["matcher"]; ok {
				_ = json.Unmarshal(raw, &matcher)
			}
			var kept []map[string]json.RawMessage
			for _, e := range entries {
				var cmd string
				if c, ok := e["command"]; ok {
					_ = json.Unmarshal(c, &cmd)
				}
				if cmd != "" && m.Provides(event, matcher, cmd) {
					removed = append(removed, "hooks."+event+": "+cmd)
					continue
				}
				kept = append(kept, e)
			}
			if len(kept) == len(entries) {
				keptGroups = append(keptGroups, group)
				continue
			}
			if len(kept) == 0 {
				continue
			}
			b, err := marshalLiteral(kept)
			if err != nil {
				return nil, nil, err
			}
			group["hooks"] = b
			keptGroups = append(keptGroups, group)
		}
		if len(keptGroups) == 0 {
			continue
		}
		b, err := marshalLiteral(keptGroups)
		if err != nil {
			return nil, nil, err
		}
		out[event] = b
	}
	return out, removed, nil
}

func prunePermissions(raw json.RawMessage, m ManagedSet) (map[string]json.RawMessage, []string, error) {
	var perms map[string]json.RawMessage
	if err := json.Unmarshal(raw, &perms); err != nil {
		return nil, nil, fmt.Errorf("parse permissions: %w", err)
	}
	rawDeny, ok := perms["deny"]
	if !ok {
		return perms, nil, nil
	}
	var deny []string
	if err := json.Unmarshal(rawDeny, &deny); err != nil {
		return nil, nil, fmt.Errorf("parse permissions.deny: %w", err)
	}
	var kept, removed []string
	for _, d := range deny {
		if m.Denies(d) {
			removed = append(removed, "permissions.deny: "+d)
			continue
		}
		kept = append(kept, d)
	}
	if len(kept) == 0 {
		delete(perms, "deny")
		return perms, removed, nil
	}
	b, err := marshalLiteral(kept)
	if err != nil {
		return nil, nil, err
	}
	perms["deny"] = b
	return perms, removed, nil
}

func setOrDrop(top map[string]json.RawMessage, key string, value map[string]json.RawMessage) {
	if len(value) == 0 {
		delete(top, key)
		return
	}
	b, _ := marshalLiteral(value)
	top[key] = b
}

// encodeIndented and marshalLiteral keep <, > and & literal: hook
// commands often carry shell redirections, and json.Marshal would
// rewrite them as \u003c escapes that survive into the output.
func encodeIndented(v any) ([]byte, error) {
	return encode(v, "  ")
}

func marshalLiteral(v any) ([]byte, error) {
	b, err := encode(v, "")
	return bytes.TrimSuffix(b, []byte("\n")), err
}

func encode(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ErrSymlinkSettings is returned by PruneSettingsFile for a settings
// file that is a symlink. Its owner (typically home-manager's
// programs.claude-code) would restore the pruned entries on its next
// switch, and replacing the link with a regular file aborts that
// switch, so the source has to be pruned instead.
var ErrSymlinkSettings = errors.New("is a symlink, managed elsewhere; prune its source instead")

// PruneSettingsFile prunes path in place. Before writing it copies the
// original to <path>.spore-bak-<UTC stamp>. A missing file, or one with
// nothing to prune, is left alone and reports no backup. A symlink is
// left alone and reported as ErrSymlinkSettings.
func PruneSettingsFile(path string, m ManagedSet, now time.Time) (removed []string, backup string, err error) {
	linfo, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("%s %w", path, ErrSymlinkSettings)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	out, removed, err := PruneSettings(raw, m)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	if len(removed) == 0 {
		return nil, "", nil
	}
	mode := linfo.Mode().Perm()
	backup = path + ".spore-bak-" + now.UTC().Format("20060102T150405Z")
	if err := os.WriteFile(backup, raw, mode); err != nil {
		return nil, "", fmt.Errorf("backup: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return nil, "", err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return nil, "", err
	}
	if err := tmp.Close(); err != nil {
		return nil, "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, "", err
	}
	return removed, backup, nil
}
