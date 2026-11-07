package localconfig

import (
	"errors"
	"path/filepath"
	"regexp"
)

// MobilePushConfig describes the APNs key kept in the server's private state.
type MobilePushConfig struct {
	BundleID string `json:"bundle_id,omitempty"`
	TeamID   string `json:"team_id"`
	KeyID    string `json:"key_id"`
	Sandbox  bool   `json:"sandbox"`
}

const defaultMobileBundleID = "dev.buddy.ios"

var mobileBundleID = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)

func (c MobilePushConfig) Topic() string {
	if c.BundleID == "" {
		return defaultMobileBundleID
	}
	return c.BundleID
}

var appleKeyID = regexp.MustCompile(`^[A-Z0-9]{10}$`)

func (c MobilePushConfig) Key(stateDir string) ([]byte, error) {
	if !appleKeyID.MatchString(c.TeamID) || !appleKeyID.MatchString(c.KeyID) {
		return nil, errors.New("set the 10-character Apple team_id and key_id in iphone_push")
	}
	if !mobileBundleID.MatchString(c.Topic()) || len(c.Topic()) > 255 {
		return nil, errors.New("set a valid app bundle_id in iphone_push")
	}
	return readPrivateFile(filepath.Join(stateDir, "apns.p8"))
}
