package camlock

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/volodymyrsmirnov/x100vi-skill/internal/errs"
)

// isolate points os.TempDir() at a fresh folder, so a real x100vi running alongside is not disturbed.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, v := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(v, dir)
	}
	saved := Timeout
	t.Cleanup(func() { Timeout = saved })
}

func TestASecondAcquireFailsWhileTheFirstIsHeld(t *testing.T) {
	isolate(t)
	Timeout = 300 * time.Millisecond
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() {
		r, err := Acquire()
		if r != nil {
			r()
		}
		done <- err
	}()
	err = <-done
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.CameraBusy || e.Message != "another x100vi command is using the camera" ||
		e.Hint != "Wait for it to finish, then run this command again." {
		t.Fatalf("got %v", err)
	}
	release()
	release() // twice is harmless
	again, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestASecondAcquireWaitsForTheFirstToFinish(t *testing.T) {
	isolate(t)
	Timeout = 10 * time.Second
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error)
	go func() {
		r, err := Acquire()
		if r != nil {
			r()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("acquired while held: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	release()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never acquired after release")
	}
}

// The lock holds across processes: a child test process cannot take it while this one does.
func TestAnotherProcessCannotTakeTheLock(t *testing.T) {
	if os.Getenv("CAMLOCK_CHILD") == "1" {
		Timeout = 200 * time.Millisecond
		r, err := Acquire()
		if r != nil {
			r()
		}
		if !errs.Is(err, errs.CameraBusy) {
			t.Fatalf("child acquired the lock: %v", err)
		}
		return
	}
	isolate(t)
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAnotherProcessCannotTakeTheLock$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CAMLOCK_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}
