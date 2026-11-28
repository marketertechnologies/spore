package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// appendHeaderLine seeds the api-headers spool the way an external
// producer would; spore has no production writer.
func appendHeaderLine(line *headerLine) error {
	if line.Timestamp.IsZero() {
		line.Timestamp = time.Now().UTC()
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	d, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, stateDirMode); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(d, apiHeadersFilename), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}
