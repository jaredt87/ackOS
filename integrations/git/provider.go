	if err := exchangePreparedTargetAtValidatedParent(target, parentFD, tmpName, name); err != nil {
		return fmt.Errorf("atomically compare-and-replace git target: %w", err)
	}
	// The exchange is the mutation point. Revalidate the parent immediately
	// afterward as well as immediately before it; if an ancestor was replaced
	// during the exchange window, restore the anchored original inode before
	// returning failure.
	if err := validateOpenedParentDir(target, parentFD); err != nil {
		if rollbackErr := rollbackExchangedTarget(parentFD, tmpName, name, fd, stat, &preparedStat); rollbackErr != nil {
			return fmt.Errorf("restore git target after parent identity changed: %w (parent check: %v)", rollbackErr, err)
		}
		return fmt.Errorf("git target parent changed during atomic replacement: %w", err)
	}
	exchangedPath := filepath.Join(filepath.Dir(path), tmpName)
	exchangedInfo, err := os.Lstat(exchangedPath)
	if err != nil {
		if rollbackErr := rollbackExchangedTarget(parentFD, tmpName, name, fd, stat, &preparedStat); rollbackErr != nil {
			return fmt.Errorf("restore git target after exchanged inode inspection failure: %w (inspection: %v)", rollbackErr, err)
		}
		return fmt.Errorf("inspect exchanged git target: %w", err)
	}
	exchangedStat, ok := exchangedInfo.Sys().(*syscall.Stat_t)
	if !ok || uint64(exchangedStat.Dev) != uint64(stat.Dev) || uint64(exchangedStat.Ino) != uint64(stat.Ino) {
		if err := rollbackExchangedTarget(parentFD, tmpName, name, fd, stat, &preparedStat); err != nil {
			return fmt.Errorf("restore concurrently replaced git target: %w", err)
		}
		return fmt.Errorf("git target changed before atomic replacement")
	}
	if err := verifyExchangedTargetMetadata(exchangedPath, exchangedInfo, info, capturedXattrs); err != nil {
		if rollbackErr := rollbackExchangedTarget(parentFD, tmpName, name, fd, stat, &preparedStat); rollbackErr != nil {
			return fmt.Errorf("restore concurrently modified git target: %w (metadata check: %v)", rollbackErr, err)
		}
		return err
	}
	rollback := func(cause error) error {
		if rollbackErr := rollbackExchangedTarget(parentFD, tmpName, name, fd, stat, &preparedStat); rollbackErr != nil {
			return fmt.Errorf("%v (rollback: %v)", cause, rollbackErr)
		}
		return cause
	}
	exchangedFD, err := syscall.Openat(parentFD, tmpName, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return rollback(fmt.Errorf("open exchanged git target: %w", err))
	}
	exchangedFile := os.NewFile(uintptr(exchangedFD), filepath.Join(filepath.Dir(path), tmpName))
	if exchangedFile == nil {
		_ = syscall.Close(exchangedFD)
		return rollback(fmt.Errorf("open exchanged git target: invalid file descriptor"))
	}
	exchangedContent, readErr := io.ReadAll(exchangedFile)
	closeErr := exchangedFile.Close()
	if readErr != nil {
		return rollback(fmt.Errorf("read exchanged git target: %w", readErr))
	}
	if closeErr != nil {
		return rollback(fmt.Errorf("close exchanged git target: %w", closeErr))
	}
	if !bytes.Equal(exchangedContent, expected) {
		return rollback(fmt.Errorf("git target content changed before atomic replacement"))
	}
	if err := syscall.Unlinkat(parentFD, tmpName); err != nil {
		return fmt.Errorf("remove exchanged git target: %w", err)
	}
	cleanup = false
	if err := syscall.Fsync(parentFD); err != nil {
		return fmt.Errorf("sync git target directory: %w", err)
	}
	return nil
}

func validateOpenedParentDir(target Target, parentFD int) error {
	var opened syscall.Stat_t
	if err := syscall.Fstat(parentFD, &opened); err != nil {
		return fmt.Errorf("stat opened git target parent: %w", err)
	}
	rootFD, err := openRepositoryRoot(target)
	if err != nil {
		return err
	}