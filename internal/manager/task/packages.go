package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/pkg"
)

type PackagesJob struct {
	PackageManager *pkg.Manager
	OnComplete     func()
}

func (j *PackagesJob) installPackage(ctx context.Context, p models.PackageSpecInput, progress *job.Progress, operation string) error {
	defer progress.Increment()

	if err := j.PackageManager.Install(ctx, p); err != nil {
		analytics.CapturePackageInstallFailure(ctx, err, analytics.PackageFailureContext{
			Operation: operation, PackageID: p.ID, JobCorrelation: job.Correlation(ctx),
		})
		return fmt.Errorf("%s package %s: %w", operation, p.ID, err)
	}

	return nil
}

type InstallPackagesJob struct {
	PackagesJob
	Packages []*models.PackageSpecInput
}

func (j *InstallPackagesJob) Execute(ctx context.Context, progress *job.Progress) error {
	progress.SetTotal(len(j.Packages))
	var failures []error

	for _, p := range j.Packages {
		if job.IsCancelled(ctx) {
			logger.Info("Cancelled installing packages")
			return errors.Join(failures...)
		}

		logger.Infof("Installing package %s", p.ID)
		taskDesc := fmt.Sprintf("Installing %s", p.ID)
		progress.ExecuteTask(taskDesc, func() {
			if err := j.installPackage(ctx, *p, progress, "install"); err != nil {
				logger.Errorf("Error installing package %s: %v", p.ID, err)
				failures = append(failures, err)
			}
		})
	}

	if j.OnComplete != nil {
		j.OnComplete()
	}

	if len(failures) > 0 {
		logger.Infof("Completed package installation batch with %d failed packages", len(failures))
	} else {
		logger.Infof("Finished installing packages")
	}
	// Every package is attempted and OnComplete refreshes partial installs, but
	// any failed package makes the job FAILED rather than FINISHED.
	return errors.Join(failures...)
}

type UpdatePackagesJob struct {
	PackagesJob
	Packages []*models.PackageSpecInput
}

func (j *UpdatePackagesJob) Execute(ctx context.Context, progress *job.Progress) error {
	// if no packages are specified, update all
	if len(j.Packages) == 0 {
		installed, err := j.PackageManager.InstalledStatus(ctx)
		if err != nil {
			return fmt.Errorf("error getting installed packages: %w", err)
		}

		for _, p := range installed {
			if p.Upgradable() {
				j.Packages = append(j.Packages, &models.PackageSpecInput{
					ID:        p.Local.ID,
					SourceURL: p.Remote.Repository.Path(),
				})
			}
		}
	}

	progress.SetTotal(len(j.Packages))
	var failures []error

	for _, p := range j.Packages {
		if job.IsCancelled(ctx) {
			logger.Info("Cancelled updating packages")
			return errors.Join(failures...)
		}

		logger.Infof("Updating package %s", p.ID)
		taskDesc := fmt.Sprintf("Updating %s", p.ID)
		progress.ExecuteTask(taskDesc, func() {
			if err := j.installPackage(ctx, *p, progress, "update"); err != nil {
				logger.Errorf("Error updating package %s: %v", p.ID, err)
				failures = append(failures, err)
			}
		})
	}

	if j.OnComplete != nil {
		j.OnComplete()
	}

	if len(failures) > 0 {
		logger.Infof("Completed package update batch with %d failed packages", len(failures))
	} else {
		logger.Infof("Finished updating packages")
	}
	return errors.Join(failures...)
}

type UninstallPackagesJob struct {
	PackagesJob
	Packages []*models.PackageSpecInput
}

func (j *UninstallPackagesJob) Execute(ctx context.Context, progress *job.Progress) error {
	progress.SetTotal(len(j.Packages))

	for _, p := range j.Packages {
		if job.IsCancelled(ctx) {
			logger.Info("Cancelled installing packages")
			return nil
		}

		logger.Infof("Uninstalling package %s", p.ID)
		taskDesc := fmt.Sprintf("Uninstalling %s", p.ID)
		progress.ExecuteTask(taskDesc, func() {
			if err := j.PackageManager.Uninstall(ctx, *p); err != nil {
				logger.Errorf("Error uninstalling package %s: %v", p.ID, err)
			}
		})
	}

	if j.OnComplete != nil {
		j.OnComplete()
	}

	logger.Infof("Finished uninstalling packages")
	return nil
}
