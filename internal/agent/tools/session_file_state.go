package tools

import (
	"context"
	"os"
	"time"

	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
)

// effectiveLastReadTime returns the tracked read time for a file, but restores
// the read state when the current on-disk content already matches the latest
// version written in this session. This prevents a session from getting stuck
// in stale-write loops after it has already created or updated a file.
func effectiveLastReadTime(
	ctx context.Context,
	sessionID string,
	filePath string,
	fileInfo os.FileInfo,
	tracker filetracker.Service,
	files history.Service,
) time.Time {
	lastRead := tracker.LastReadTime(ctx, sessionID, filePath)
	if !lastRead.IsZero() || fileInfo == nil || fileInfo.IsDir() || files == nil {
		return lastRead
	}

	historyFile, err := files.GetByPathAndSession(ctx, filePath, sessionID)
	if err != nil {
		return lastRead
	}

	currentContent, err := os.ReadFile(filePath)
	if err != nil {
		return lastRead
	}
	if string(currentContent) != historyFile.Content {
		return lastRead
	}

	tracker.RecordRead(ctx, sessionID, filePath)
	return fileInfo.ModTime().Truncate(time.Second)
}
