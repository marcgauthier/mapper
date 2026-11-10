package app

import (
	"archive/zip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcgauthier/libmurmur/clients/go/bindings/store"
	"github.com/marcgauthier/libmurmur/clients/go/murmurdb"
)

const maxBackupUpload = int64(4 << 30)

func (a *App) allowUnlockAttempt(peer string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unlockAttempts == nil {
		a.unlockAttempts = make(map[string][]time.Time)
	}
	recent := a.unlockAttempts[peer][:0]
	for _, attempt := range a.unlockAttempts[peer] {
		if now.Sub(attempt) < 5*time.Minute {
			recent = append(recent, attempt)
		}
	}
	if len(recent) >= 3 {
		a.unlockAttempts[peer] = recent
		return false
	}
	a.unlockAttempts[peer] = append(recent, now)
	return true
}

func mapperStorePath() string {
	path := os.Getenv("MAPPER_DB")
	if path == "" {
		path = "./data/mapper"
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean("./data/mapper")
	}
	return filepath.Clean(path)
}

func databaseKey(raw string) [32]byte { return sha256.Sum256([]byte(raw)) }

// storePath resolves the encrypted store location: the config file value
// when the server set one, otherwise MAPPER_DB or the default.
func (a *App) storePath() string {
	if a.store != "" {
		if abs, err := filepath.Abs(a.store); err == nil {
			return filepath.Clean(abs)
		}
		return filepath.Clean(a.store)
	}
	return mapperStorePath()
}

func (a *App) downloadBackup(w http.ResponseWriter, _ *http.Request) {
	base := a.storePath()
	tmp, err := os.MkdirTemp("", "mapper-backup-*")
	if err != nil {
		http.Error(w, "could not prepare database backup", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmp)
	checkpoint := filepath.Join(tmp, "checkpoint")
	group, node := [16]byte{}, [16]byte{}
	if _, err := rand.Read(group[:]); err != nil {
		http.Error(w, "could not create backup identity", http.StatusInternalServerError)
		return
	}
	if _, err := rand.Read(node[:]); err != nil {
		http.Error(w, "could not create backup identity", http.StatusInternalServerError)
		return
	}
	if err := store.BackupLive(base, checkpoint, group, node); err != nil {
		http.Error(w, "encrypted database backup failed", http.StatusInternalServerError)
		return
	}
	archivePath := filepath.Join(tmp, "mapper.murmur")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		http.Error(w, "could not prepare backup download", http.StatusInternalServerError)
		return
	}
	zw := zip.NewWriter(archive)
	err = filepath.WalkDir(checkpoint, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("native backup contains a symbolic link")
		}
		rel, err := filepath.Rel(checkpoint, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("native backup contains a non-regular file")
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Store
		out, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		http.Error(w, "could not package encrypted database backup", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(archivePath)
	if err != nil {
		http.Error(w, "could not read database backup", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "could not read database backup", http.StatusInternalServerError)
		return
	}
	filename := "mapper-" + time.Now().UTC().Format("20060102T150405Z") + ".murmur"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(stat.Size()))
	_, _ = io.Copy(w, file)
}

func (a *App) restore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.storeMu.Lock()
	defer a.storeMu.Unlock()
	a.mu.RLock()
	locked := a.db == nil
	a.mu.RUnlock()
	if !locked {
		http.Error(w, "lock Mapper before restoring a database backup", http.StatusConflict)
		return
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if !a.allowUnlockAttempt(peer, time.Now()) {
		http.Error(w, "too many unlock and restore attempts; wait five minutes", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "backup upload is invalid or too large", http.StatusBadRequest)
		return
	}
	keyText := r.FormValue("key")
	if len(keyText) < 8 {
		http.Error(w, "database key must contain at least 8 characters", http.StatusBadRequest)
		return
	}
	input, header, err := r.FormFile("backup")
	if err != nil {
		http.Error(w, "choose a Mapper .murmur backup file", http.StatusBadRequest)
		return
	}
	defer input.Close()
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".murmur") || header.Size <= 0 {
		http.Error(w, "choose a Mapper .murmur backup file", http.StatusBadRequest)
		return
	}
	archive, err := zip.NewReader(input, header.Size)
	if err != nil {
		http.Error(w, "backup file is not a valid Mapper archive", http.StatusBadRequest)
		return
	}
	key := databaseKey(keyText)
	tmp, err := os.MkdirTemp(filepath.Dir(a.storePath()), ".mapper-restore-*")
	if err != nil {
		http.Error(w, "could not prepare restore", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmp)
	checkpoint := filepath.Join(tmp, "checkpoint")
	if err := extractBackupZip(archive, checkpoint); err != nil {
		http.Error(w, "backup contents are invalid: "+err.Error(), http.StatusBadRequest)
		return
	}
	restored := filepath.Join(tmp, "restored")
	if err := store.Restore(checkpoint, restored, key); err != nil {
		http.Error(w, "backup verification failed; check the database key and file", http.StatusBadRequest)
		return
	}
	key = databaseKey(keyText)
	probe, err := murmurdb.Open(restored + "?key=" + hex.EncodeToString(key[:]) + "&mode=rw")
	clear(key[:])
	if err != nil {
		http.Error(w, "restored database could not be opened", http.StatusBadRequest)
		return
	}
	if err = probe.Ping(); err == nil {
		err = migrateSchema(probe)
	}
	if err == nil {
		var users int
		err = probe.QueryRow(`SELECT count(*) FROM users`).Scan(&users)
		if err == nil && users == 0 {
			err = fmt.Errorf("backup contains no configured users")
		}
	}
	closeErr := probe.Close()
	if err != nil || closeErr != nil {
		http.Error(w, "restored database validation failed", http.StatusBadRequest)
		return
	}
	if err = replaceStore(a.storePath(), restored); err != nil {
		http.Error(w, "could not install restored database", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"success":true}`))
}

func extractBackupZip(archive *zip.Reader, destination string) error {
	if len(archive.File) == 0 || len(archive.File) > 20000 {
		return fmt.Errorf("unexpected file count")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	var total uint64
	manifest := false
	for _, file := range archive.File {
		name := filepath.FromSlash(file.Name)
		clean := filepath.Clean(name)
		if name == "" || filepath.IsAbs(name) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive path")
		}
		if file.Mode()&os.ModeSymlink != 0 || (!file.FileInfo().IsDir() && !file.Mode().IsRegular()) {
			return fmt.Errorf("unsupported archive entry")
		}
		total += file.UncompressedSize64
		if total > 8<<30 {
			return fmt.Errorf("archive expands beyond the allowed size")
		}
		target := filepath.Join(destination, clean)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if clean == "manifest.json" {
			manifest = true
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(in, int64(file.UncompressedSize64)+1))
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
	}
	if !manifest {
		return fmt.Errorf("manifest is missing")
	}
	return nil
}

func replaceStore(destination, source string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	old := destination + ".pre-restore"
	if _, err := os.Lstat(old); err == nil {
		return fmt.Errorf("previous restore staging path exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	hadOld := false
	if _, err := os.Lstat(destination); err == nil {
		if err := os.Rename(destination, old); err != nil {
			return err
		}
		hadOld = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		if hadOld {
			_ = os.Rename(old, destination)
		}
		return err
	}
	if hadOld {
		if err := os.RemoveAll(old); err != nil {
			return fmt.Errorf("restored database installed but old store cleanup failed: %w", err)
		}
	}
	return nil
}
