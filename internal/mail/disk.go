package mail

import "time"

type DiskState struct {
	UsedPercent  int       `json:"used_percent"`
	Level        string    `json:"level"`
	MetadataOnly bool      `json:"metadata_only"`
	CheckedAt    time.Time `json:"checked_at"`
}

func classifyDisk(usedPercent int, checkedAt time.Time) DiskState {
	state := DiskState{UsedPercent: usedPercent, Level: "normal", CheckedAt: checkedAt.UTC()}
	switch {
	case usedPercent >= 90:
		state.Level = "metadata_only"
		state.MetadataOnly = true
	case usedPercent >= 85:
		state.Level = "critical"
	case usedPercent >= 70:
		state.Level = "warning"
	}
	return state
}
