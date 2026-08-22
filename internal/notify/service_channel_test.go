package notify

import "testing"

func TestOverlaySecretClearsOptionalSettings(t *testing.T) {
	current := channelSecret{
		PushPlusToken: "push-token", PushPlusTopic: "old-topic",
		BarkServerURL: "https://api.day.app", BarkDeviceKey: "device-key",
		BarkGroup: "old-group", BarkSound: "minuet",
	}
	overlaySecret(&current, channelSecret{})

	if current.PushPlusToken != "push-token" || current.BarkServerURL != "https://api.day.app" || current.BarkDeviceKey != "device-key" {
		t.Fatalf("required credentials were cleared: %#v", current)
	}
	if current.PushPlusTopic != "" || current.BarkGroup != "" || current.BarkSound != "" {
		t.Fatalf("optional settings were not cleared: %#v", current)
	}
}
