package protocol

// RetentionDays configures how long unreferenced vault objects are retained.
type RetentionDays struct {
	Raw      int `json:"raw"`
	Stale    int `json:"stale"`
	Blob     int `json:"blob"`
	Redacted int `json:"redacted"`
}

type StorageSettingsResult struct {
	Retention RetentionDays `json:"retention"`
	Source    string        `json:"source"`
	Stats     VaultStats    `json:"stats"`
}

type StorageCleanupResult struct {
	Deleted    int        `json:"deleted"`
	FreedBytes int64      `json:"freedBytes"`
	Stats      VaultStats `json:"stats"`
}
