package cli

import (
	"testing"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/notify"
)

func TestNotifyWillAttachGate(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.NotifyConfig
		want bool
	}{
		{"disabled", config.NotifyConfig{Enabled: false}, false},
		{"enabled no channel", config.NotifyConfig{Enabled: true}, false},
		{
			"enabled bark without key",
			config.NotifyConfig{Enabled: true, Bark: []config.BarkConfig{{Enabled: true}}},
			false,
		},
		{
			"enabled bark disabled",
			config.NotifyConfig{Enabled: true, Bark: []config.BarkConfig{{Enabled: false, Key: "k"}}},
			false,
		},
		{
			"enabled bark with key",
			config.NotifyConfig{Enabled: true, Bark: []config.BarkConfig{{Enabled: true, Key: "k"}}},
			true,
		},
	}
	for _, c := range cases {
		if got := notifyWillAttach(c.cfg); got != c.want {
			t.Errorf("%s: notifyWillAttach = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildNotifyChannelsSkipsBadChannel(t *testing.T) {
	cfg := config.NotifyConfig{
		Enabled: true,
		Bark: []config.BarkConfig{
			{Name: "good", Enabled: true, Key: "k1", BaseURL: "https://api.day.app"},
			{Name: "disabled", Enabled: false, Key: "k2"},
			{
				Name:    "badenc",
				Enabled: true,
				Key:     "k3",
				Encryption: &config.BarkEncryptionConfig{
					Algorithm: "AES256", Mode: "CBC", Key: "tooshort",
				},
			},
		},
	}
	specs, warns := buildNotifyChannels(cfg)
	if len(specs) != 1 {
		t.Errorf("want 1 usable channel, got %d", len(specs))
	}
	if len(warns) != 1 {
		t.Errorf("want 1 warning for the bad-encryption channel, got %d: %v", len(warns), warns)
	}
}

func TestBuildBarkChannelSpecMinPriority(t *testing.T) {
	spec, warn := buildBarkChannelSpec(config.BarkConfig{
		Name: "phone", Enabled: true, Key: "k", MinPriority: "high",
	})
	if warn != "" {
		t.Fatalf("unexpected warn: %s", warn)
	}
	if spec.MinPriority != notify.PriorityHigh {
		t.Errorf("MinPriority = %v, want High", spec.MinPriority)
	}
	if spec.Channel == nil {
		t.Error("Channel should be constructed")
	}
}
