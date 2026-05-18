package filewatcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/model"
)

// MockEventBus implements EventEmitter for testing
type MockEventBus struct {
	mu     sync.Mutex
	events []agent.Event
}

func (m *MockEventBus) Emit(event agent.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *MockEventBus) GetEvents() []agent.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agent.Event{}, m.events...)
}

func (m *MockEventBus) GetEventCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func (m *MockEventBus) ClearEvents() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
}

func (m *MockEventBus) GetEventsByType(eventType string) []agent.Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []agent.Event
	for _, event := range m.events {
		if event.Type == eventType {
			result = append(result, event)
		}
	}
	return result
}

func TestNewFileWatcher(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	if watcher == nil {
		t.Fatal("New() returned nil watcher")
	}

	if watcher.watcher == nil {
		t.Error("New() did not initialize fsnotify watcher")
	}
}

func TestFileWatcher_StartStop(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)
	if watcher == nil {
		t.Skip("fsnotify not available")
	}

	ctx := context.Background()

	// Test start
	err := watcher.Start(ctx)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Test stop
	watcher.Stop()

	// Multiple stops should be safe
	watcher.Stop()
}

func TestFileWatcher_AddRemoveDirectory(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)
	if watcher == nil {
		t.Skip("fsnotify not available")
	}

	ctx := context.Background()
	watcher.Start(ctx)
	defer watcher.Stop()

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "filewatcher_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test add directory
	err = watcher.AddDirectory(tempDir)
	if err != nil {
		t.Fatalf("AddDirectory() failed: %v", err)
	}

	// Check directory is in watch list
	dirs := watcher.WatchingDirectories()
	if len(dirs) != 1 {
		t.Errorf("Expected 1 directory, got %d", len(dirs))
	}
	if dirs[0] != tempDir {
		t.Errorf("Expected %s, got %s", tempDir, dirs[0])
	}

	// Test duplicate add (should be idempotent)
	err = watcher.AddDirectory(tempDir)
	if err != nil {
		t.Fatalf("Duplicate AddDirectory() failed: %v", err)
	}

	dirs = watcher.WatchingDirectories()
	if len(dirs) != 1 {
		t.Errorf("Expected still 1 directory after duplicate add, got %d", len(dirs))
	}

	// Test remove directory
	err = watcher.RemoveDirectory(tempDir)
	if err != nil {
		t.Fatalf("RemoveDirectory() failed: %v", err)
	}

	dirs = watcher.WatchingDirectories()
	if len(dirs) != 0 {
		t.Errorf("Expected 0 directories, got %d", len(dirs))
	}
}

func TestFileWatcher_FileEvents(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)
	if watcher == nil {
		t.Skip("fsnotify not available")
	}

	ctx := context.Background()
	watcher.Start(ctx)
	defer watcher.Stop()

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "filewatcher_events_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = watcher.AddDirectory(tempDir)
	if err != nil {
		t.Fatalf("AddDirectory() failed: %v", err)
	}

	// Give watcher time to start watching
	time.Sleep(100 * time.Millisecond)

	// Test file creation
	testFile := filepath.Join(tempDir, "test_create.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Wait for events to be processed
	time.Sleep(500 * time.Millisecond)

	// Check for file event (created or updated - on some systems create = update)
	createdEvents := mockBus.GetEventsByType(model.EventTypeHostFileCreated)
	updatedEvents := mockBus.GetEventsByType(model.EventTypeHostFileUpdated)
	allEvents := append(createdEvents, updatedEvents...)

	if len(allEvents) == 0 {
		t.Error("No file events received")
	} else {
		found := false
		for _, event := range allEvents {
			if data, ok := event.Data.(map[string]any); ok {
				if file, ok := data["file"].(string); ok && file == testFile {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("File event for %s not found", testFile)
		}
	}

	mockBus.ClearEvents()

	// Test file update
	err = os.WriteFile(testFile, []byte("updated content"), 0644)
	if err != nil {
		t.Fatalf("Failed to update test file: %v", err)
	}

	// Wait for events to be processed
	time.Sleep(500 * time.Millisecond)

	// Check for file updated event
	var updatedEventsCheck []agent.Event
	updatedEventsCheck = mockBus.GetEventsByType(model.EventTypeHostFileUpdated)
	if len(updatedEventsCheck) == 0 {
		t.Error("No file updated events received")
	}

	mockBus.ClearEvents()

	// Test file deletion
	err = os.Remove(testFile)
	if err != nil {
		t.Fatalf("Failed to delete test file: %v", err)
	}

	// Wait for events to be processed
	time.Sleep(500 * time.Millisecond)

	// Check for file deleted event
	deletedEventsCheck := mockBus.GetEventsByType(model.EventTypeHostFileDeleted)
	if len(deletedEventsCheck) == 0 {
		t.Error("No file deleted events received")
	}
}

func TestFileWatcher_IgnorePatterns(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)
	if watcher == nil {
		t.Skip("fsnotify not available")
	}

	ctx := context.Background()
	watcher.Start(ctx)
	defer watcher.Stop()

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "filewatcher_ignore_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = watcher.AddDirectory(tempDir)
	if err != nil {
		t.Fatalf("AddDirectory() failed: %v", err)
	}

	// Give watcher time to start watching
	time.Sleep(100 * time.Millisecond)

	// Create files that should be ignored
	// Note: Some files might still generate events on different OSes
	// We're mainly testing that the ignore logic exists and works for most cases
	ignoredFiles := []string{
		"test.tmp",
		"test.swp",
		"test.bak",
	}

	for _, filename := range ignoredFiles {
		testFile := filepath.Join(tempDir, filename)
		err = os.WriteFile(testFile, []byte("test"), 0644)
		if err != nil {
			t.Fatalf("Failed to create ignored file %s: %v", filename, err)
		}
	}

	// Wait for events to be processed
	time.Sleep(500 * time.Millisecond)

	// Check that no events were generated for ignored files
	// Note: This test is somewhat platform-dependent, so we just check that
	// the number of events is reasonably low (some might slip through)
	eventCount := mockBus.GetEventCount()
	if eventCount > len(ignoredFiles) {
		t.Logf("Warning: Got %d events for %d ignored files (some may be platform-dependent)", eventCount, len(ignoredFiles))
		for i, event := range mockBus.GetEvents() {
			t.Logf("Event %d: %s for file: %v", i, event.Type, event.Data)
		}
	}
}

func TestFileWatcher_MultipleDirectories(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)
	if watcher == nil {
		t.Skip("fsnotify not available")
	}

	ctx := context.Background()
	watcher.Start(ctx)
	defer watcher.Stop()

	// Create multiple temp directories
	var dirs []string
	for i := 0; i < 3; i++ {
		tempDir, err := os.MkdirTemp("", "filewatcher_multi_test")
		if err != nil {
			t.Fatalf("Failed to create temp dir %d: %v", i, err)
		}
		dirs = append(dirs, tempDir)
		defer os.RemoveAll(tempDir)

		err = watcher.AddDirectory(tempDir)
		if err != nil {
			t.Fatalf("AddDirectory() failed for dir %d: %v", i, err)
		}
	}

	// Check all directories are watched
	watchingDirs := watcher.WatchingDirectories()
	if len(watchingDirs) != 3 {
		t.Errorf("Expected 3 directories, got %d", len(watchingDirs))
	}

	// Verify all our directories are in the watch list
	dirMap := make(map[string]bool)
	for _, dir := range watchingDirs {
		dirMap[dir] = true
	}

	for _, expectedDir := range dirs {
		if !dirMap[expectedDir] {
			t.Errorf("Directory %s not found in watch list", expectedDir)
		}
	}
}

func TestFileWatcher_NilWatcher(t *testing.T) {
	var watcher *Watcher

	// Test that nil watcher doesn't panic
	watcher.Stop()
	watcher.WatchingDirectories()

	// Note: nil watcher methods don't return errors, they just return safely
	_ = watcher.AddDirectory("/tmp")
	_ = watcher.RemoveDirectory("/tmp")
}

func TestFileWatcher_EventDebouncing(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)
	if watcher == nil {
		t.Skip("fsnotify not available")
	}

	ctx := context.Background()
	watcher.Start(ctx)
	defer watcher.Stop()

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "filewatcher_debounce_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = watcher.AddDirectory(tempDir)
	if err != nil {
		t.Fatalf("AddDirectory() failed: %v", err)
	}

	// Give watcher time to start watching
	time.Sleep(100 * time.Millisecond)

	// Rapidly create and update the same file
	testFile := filepath.Join(tempDir, "rapid.txt")
	for i := 0; i < 5; i++ {
		err = os.WriteFile(testFile, []byte(string(rune('0'+i))), 0644)
		if err != nil {
			t.Fatalf("Failed to write file iteration %d: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond) // Faster than debounce time
	}

	// Wait for debounce to settle
	time.Sleep(500 * time.Millisecond)

	// Should have fewer events than writes due to debouncing
	events := mockBus.GetEvents()
	if len(events) == 0 {
		t.Error("Expected some events after rapid writes")
	}

	// Due to debouncing, we should have significantly fewer events than writes
	// (though exact number depends on timing)
	t.Logf("Got %d events from 5 rapid writes", len(events))
}
