# Claude Code and Codex agent assets for the spore-fleet module: the
# host-wide skill links, the Claude Code managed settings drop-in, and
# the local spore plugin marketplace. Imported by spore-fleet.nix.
{ config, lib, pkgs, ... }:

let
  cfg = config.services.spore-fleet;

  # Skill bodies come from cfg.package (`spore install --dest`), so the
  # binary stays the single source. The name list comes from this
  # module's source tree (same flake revision as the default package)
  # to avoid import-from-derivation; the build fails if the two drift.
  skillNames = lib.attrNames (lib.filterAttrs (_: type: type == "directory")
    (builtins.readDir ../bootstrap/skills));
  sporeSkills = pkgs.runCommand "spore-skills" { } ''
    ${cfg.package}/bin/spore install --dest $out > /dev/null
    got="$(cd $out && LC_ALL=C ls -1 | tr '\n' ' ')"
    want="${lib.concatStringsSep " " skillNames} "
    if [ "$got" != "$want" ]; then
      echo "spore-skills: package ships [$got], module expects [$want]" >&2
      exit 1
    fi
  '';

  # Local Claude Code plugin marketplace: the repo's .claude-plugin/
  # manifests plus the package's skills at the path plugin.json names.
  # Claude Code loads a relative-path plugin from a directory
  # marketplace in place, so the store copy is what runs.
  claudeMarketplace = pkgs.runCommand "spore-claude-marketplace" { } ''
    mkdir -p $out/.claude-plugin $out/bootstrap
    cp ${../.claude-plugin/plugin.json} $out/.claude-plugin/plugin.json
    cp ${../.claude-plugin/marketplace.json} $out/.claude-plugin/marketplace.json
    cp -r ${sporeSkills} $out/bootstrap/skills
  '';

  # Rendered by the spore CLI (internal/hooks/hostsettings.go), the same
  # source bootstrap/handover/settings.json is tested against. Hook
  # commands pin cfg.package and the shims package, so they track every
  # rebuild instead of going stale under ~/.claude/hooks.
  managedSettings = pkgs.runCommand "spore-managed-settings.json" { } ''
    ${cfg.package}/bin/spore hooks settings --managed \
      --spore ${cfg.package}/bin/spore \
      --hooks-dir ${cfg.shimsPackage}/share/spore/hooks \
      ${lib.optionalString cfg.claudeManagedSettings.plugin.enable "--marketplace ${claudeMarketplace}"} \
      > $out
  '';

  # One directory link per skill: Codex skips file symlinks during skill
  # discovery (so home.file `recursive` is out), and per-name links
  # leave unmanaged siblings such as ~/.claude/skills/synced alone.
  # ~/.agents/skills is Codex's user scope; it is read from $HOME, not
  # CODEX_HOME, so per-session coordinator homes see it unchanged.
  skillFiles = lib.listToAttrs (lib.concatMap
    (root: map
      (name: lib.nameValuePair "${root}/${name}" { source = "${sporeSkills}/${name}"; })
      skillNames)
    [ ".claude/skills" ".agents/skills" ]);
in
{
  options.services.spore-fleet.claudeManagedSettings = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Write `/etc/claude-code/managed-settings.d/50-spore.json`,
        a Claude Code managed settings drop-in rendered by
        `spore hooks settings --managed`. It carries spore's
        Notification, PreToolUse, SessionStart and Stop hooks
        (pointing at this package and the shims package in the
        Nix store) and `permissions.deny`. Managed settings apply
        to every Claude Code session on the host and sit above
        user and project settings; hooks from all scopes still
        run, so migration 004 prunes the same entries from
        `~/.claude/settings.json`. Only takes effect with
        `services.spore-fleet.enable`.
      '';
    };

    plugin.enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Also register a Nix-store copy of the repo's
        `.claude-plugin/` marketplace as the `spore` marketplace
        and force-enable `spore@spore` in the drop-in. Plugin
        skills are namespaced (`/spore:select-agent`), so this
        is off by default: the home-manager links under
        `~/.claude/skills` keep the bare names that roles and
        docs use, and Codex needs those links regardless. With
        both on, each skill shows up under both names.
      '';
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      home-manager.users.${cfg.user}.home.file = skillFiles;
    })

    (lib.mkIf (cfg.enable && cfg.claudeManagedSettings.enable) {
      environment.etc."claude-code/managed-settings.d/50-spore.json".source = managedSettings;
    })
  ];
}
