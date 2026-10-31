package main

import (
	"io"
	"testing"
	"time"

	"github.com/cristianradulescu/portview/internal/inventory"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    config
		wantErr bool
	}{
		{name: "defaults", args: nil, want: config{interval: 2 * time.Second, state: inventory.StateListening}},
		{
			name: "all flags",
			args: []string{"--interval", "5s", "--no-docker", "--dump", "--state", "all"},
			want: config{interval: 5 * time.Second, noDocker: true, dump: true, state: inventory.StateAll},
		},
		{name: "interval too small", args: []string{"--interval", "10ms"}, wantErr: true},
		{name: "bad state", args: []string{"--state", "closed"}, wantErr: true},
		{name: "positional args", args: []string{"extra"}, wantErr: true},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFlags(tt.args, io.Discard)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
