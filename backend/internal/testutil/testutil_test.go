package testutil

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func TestWAV(t *testing.T) {
	b := WAV(1500)
	if len(b) != 44+24000 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" ||
		binary.LittleEndian.Uint32(b[40:]) != 24000 || binary.LittleEndian.Uint32(b[28:]) != 16000 {
		t.Fatal("bad WAV header")
	}
}

func TestFixtures(t *testing.T) {
	pool := DB(t)
	ctx := context.Background()
	u := CreateUser(t, pool, 120)
	m := CreateMeeting(t, pool, u.ID)
	sum := sha256.Sum256([]byte(u.Token))
	var owner string
	if err := pool.QueryRow(ctx, `SELECT s.user_id FROM sessions s JOIN meetings m ON m.user_id = s.user_id
		WHERE s.token_hash = $1 AND m.id = $2`, sum[:], m).Scan(&owner); err != nil || owner != u.ID {
		t.Fatal(owner, err)
	}
	var q int64
	if err := pool.QueryRow(ctx, `SELECT quota_seconds FROM entitlements WHERE user_id=$1 AND now() >= period_start AND now() < period_end`, u.ID).Scan(&q); err != nil || q != 120 {
		t.Fatal(q, err)
	}
	if c := Config(t); !c.DevLoginEnabled || c.STTProvider != "fake" {
		t.Fatal("config")
	}
}
