package notify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

func TestStatusCountsFailuresInARowAndKeepsLastSuccess(t *testing.T) {
	dir := t.TempDir()
	RecordStatus(dir, "tg", nil)
	RecordStatus(dir, "tg", errors.New("telegram answered HTTP 500\nsecond line"))
	RecordStatus(dir, "tg", errors.New("telegram answered HTTP 500"))
	st := ReadStatus(dir)["tg"]
	if st.OK || st.Failures != 2 || st.LastOK.IsZero() || st.Error != "telegram answered HTTP 500" {
		t.Fatalf("%+v", st)
	}
	RecordStatus(dir, "tg", nil)
	if st = ReadStatus(dir)["tg"]; !st.OK || st.Failures != 0 {
		t.Fatalf("after success: %+v", st)
	}
	ForgetStatus(dir, "tg")
	if _, ok := ReadStatus(dir)["tg"]; ok {
		t.Error("not forgotten")
	}
}

func TestStatusUpdatesFromManyWritersAreNotLost(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); RecordStatus(dir, fmt.Sprintf("c%02d", i), nil) }()
	}
	wg.Wait()
	if n := len(ReadStatus(dir)); n != 20 {
		t.Errorf("%d of 20 survived", n)
	}
}

func TestStatusWaitsForAnotherProcessHoldingTheLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := paths.Lock(context.Background(), filepath.Join(dir, statusFile+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { RecordStatus(dir, "a", nil); close(done) }()
	select {
	case <-done:
		t.Fatal("wrote while the lock was held elsewhere")
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("did not write after the lock was released")
	}
	if _, ok := ReadStatus(dir)["a"]; !ok {
		t.Error("lost")
	}
}
