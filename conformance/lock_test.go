//go:build conformance

// The tag keeps this file out of the untagged build: syscall.Flock exists on Linux and macOS, where the battery runs,
// and not on Windows, where go vet ./... must still build the package.

package conformance

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"
)

// lockWait is the longest a run waits for the lock another run holds: room for the worlds of many runs, a run of every
// entry's files, five worlds, taking about 19 s on one arm64 Mac.
const lockWait = 5 * time.Minute

// runTime is what a run keeps for its worlds after the wait, under go test's -timeout: a run of every entry's files,
// five worlds, took about 19 s on one arm64 Mac, and a first run also pulls the proxies' images. -timeout ends the test
// binary in a panic that removes no world, so the wait stops in time for the run to say why itself and remove what it
// made.
const runTime = 2 * time.Minute

// lockPath is the lock's file, one path for every run on the machine. It is not in the temporary folder, because that
// folder comes from TMPDIR, which differs between two users (macOS gives each its own) and can differ between two runs
// of one user, while the Docker names the lock guards are one set on the machine: two runs that locked files in two
// temporary folders would remove each other's containers. /tmp exists on Linux and macOS, where the battery runs.
const lockPath = "/tmp/proxyfile-conformance.lock"

// lockWorld takes the conformance lock of this machine for the rest of owner, the test whose subtests claim every
// world of the run, waiting while another run holds it; t is the test that needs the lock now, which fails when it
// cannot be had. The battery's Docker names and subnet are fixed, so two runs on one machine would remove each
// other's containers; each run claims its worlds under the lock instead, and a second run waits for the first. A run
// takes it once and gives it back once, so it waits once, for at most lockWait and never past go test's -timeout less
// runTime. The lock is an exclusive flock on lockPath, made when absent and never removed: a run that removed it would
// let a third run lock a new file of the same name while a second still held the old one. The kernel gives back the
// lock of a process that dies, so the run after a killed one takes it at once, and its claimWorld removes what the
// killed run left. Every world is claimed in a subtest of owner, so it is removed before the lock is given back
// (cleanups run when a test and all its subtests end).
func lockWorld(t, owner *testing.T) {
	f, err := openLock()
	if err != nil {
		t.Fatalf("opening the conformance lock: %v", err)
	}
	wait := lockWait
	if deadline, ok := t.Deadline(); ok {
		wait = max(min(wait, time.Until(deadline)-runTime), 0)
	}
	start := time.Now()
	waited := false
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			t.Fatalf("locking %s: %v", lockPath, err)
		}
		if !waited {
			t.Logf("waiting for the conformance lock %s, which another run on this machine holds", lockPath)
			waited = true
		}
		if time.Since(start) >= wait {
			f.Close()
			t.Fatalf("another conformance run on this machine has held %s for %s, as long as this run may wait under go test's "+
				"-timeout; wait for it to end, or end it", lockPath, wait.Round(time.Second))
		}
		time.Sleep(500 * time.Millisecond)
	}
	// The cleanup holds f, so the file stays open and the lock held until owner ends: a file the garbage collector
	// closed would give the lock back early.
	owner.Cleanup(func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	})
}

// openLock opens lockPath read only, which is all flock needs, and creates it when absent. It opens before it creates,
// because Linux refuses an open with O_CREATE of another user's file in a folder such as /tmp while
// fs.protected_regular is set (the kernel's Documentation/admin-guide/sysctl/fs.rst). The run that creates the file
// makes it readable by everyone whatever its umask, so another user's run can open it too.
func openLock() (*os.File, error) {
	f, err := os.Open(lockPath)
	if !errors.Is(err, fs.ErrNotExist) {
		return f, err
	}
	f, err = os.OpenFile(lockPath, os.O_RDONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		// Another run created it after the first open: O_EXCL fails rather than open that run's file.
		return os.Open(lockPath)
	}
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
