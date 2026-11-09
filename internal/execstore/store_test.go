package execstore

import "testing"

func TestExecLifecycleAndSingleStart(t *testing.T) {
	store := New()
	created, err := store.Create("container-id", []string{"sh", "-c", "exit 42"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.State != Created {
		t.Fatalf("unexpected initial record: %#v", created)
	}
	started, err := store.Start(created.ID)
	if err != nil || started.State != Running {
		t.Fatalf("Start() = (%#v, %v)", started, err)
	}
	if _, err := store.Start(created.ID); err == nil {
		t.Fatal("a running exec was started a second time")
	}
	completed, err := store.Complete(created.ID, 42)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != Exited || completed.ExitCode == nil || *completed.ExitCode != 42 {
		t.Fatalf("unexpected completed record: %#v", completed)
	}
}
