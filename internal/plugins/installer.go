package plugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shaktsin/umcode/internal/store"
)

type InstallMode string

const (
	InstallManaged InstallMode = "managed"
	InstallLinked  InstallMode = "linked"
)

type InspectRequest struct {
	Source string
	Mode   InstallMode
}

type Inspection struct {
	Token            string
	ExpiresAt        time.Time
	Source           string
	SourceID         string
	Digest           string
	Mode             InstallMode
	Package          Package
	RequiresApproval bool
}

type inspectionEntry struct {
	inspection     Inspection
	originalSource string
}

type Installer struct {
	home string
	st   *store.Store
	now  func() time.Time

	mu          sync.Mutex
	inspections map[string]inspectionEntry
}

func NewInstaller(home string, st *store.Store, now func() time.Time) *Installer {
	if now == nil {
		now = time.Now
	}
	return &Installer{home: home, st: st, now: now, inspections: map[string]inspectionEntry{}}
}

func (i *Installer) Inspect(ctx context.Context, req InspectRequest) (Inspection, error) {
	if req.Mode == "" {
		req.Mode = InstallManaged
	}
	if req.Mode != InstallManaged && req.Mode != InstallLinked {
		return Inspection{}, pluginError("inspect", "inspect/invalid_mode", "mode", "unsupported plugin installation mode", "Choose managed or linked mode.", nil)
	}
	if req.Mode == InstallLinked && isGitSource(req.Source) {
		return Inspection{}, pluginError("inspect", "inspect/linked_requires_local", "source", "linked plugins require a local directory", "Use managed mode for Git sources.", nil)
	}
	materialized, err := materializeSource(ctx, req.Source)
	if err != nil {
		return Inspection{}, err
	}
	defer materialized.cleanup()
	digest, err := digestTree(materialized.root)
	if err != nil {
		return Inspection{}, err
	}
	pkg, err := LoadPackage(materialized.root)
	if err != nil {
		return Inspection{}, err
	}
	token, err := randomToken()
	if err != nil {
		return Inspection{}, err
	}
	inspection := Inspection{
		Token: token, ExpiresAt: i.now().UTC().Add(15 * time.Minute), Source: materialized.canonical,
		SourceID: sourceID(materialized.canonical), Digest: digest, Mode: req.Mode, Package: pkg,
		RequiresApproval: packageRequiresApproval(pkg),
	}
	i.mu.Lock()
	i.inspections[token] = inspectionEntry{inspection: inspection, originalSource: req.Source}
	i.mu.Unlock()
	return inspection, nil
}

func (i *Installer) Install(ctx context.Context, token, projectID string) (store.PluginInstallation, error) {
	i.mu.Lock()
	entry, ok := i.inspections[token]
	i.mu.Unlock()
	if !ok {
		return store.PluginInstallation{}, pluginError("install", "install/invalid_token", "inspection", "inspection token is invalid or already used", "Inspect the plugin again.", nil)
	}
	if i.now().UTC().After(entry.inspection.ExpiresAt) {
		return store.PluginInstallation{}, pluginError("install", "install/inspection_expired", "inspection", "inspection token has expired", "Inspect the plugin again.", nil)
	}
	materialized, err := materializeSource(ctx, entry.originalSource)
	if err != nil {
		return store.PluginInstallation{}, err
	}
	defer materialized.cleanup()
	digest, err := digestTree(materialized.root)
	if err != nil {
		return store.PluginInstallation{}, err
	}
	if digest != entry.inspection.Digest || materialized.canonical != entry.inspection.Source {
		return store.PluginInstallation{}, pluginError("install", "install/source_changed", "source", "plugin source changed after inspection", "Inspect the changed source again before installing.", nil)
	}
	pkg, err := LoadPackage(materialized.root)
	if err != nil {
		return store.PluginInstallation{}, err
	}

	root := materialized.root
	createdRoot := false
	if entry.inspection.Package.Format != pkg.Format || entry.inspection.Package.ID != pkg.ID {
		return store.PluginInstallation{}, pluginError("install", "install/source_changed", "manifest", "plugin identity changed after inspection", "Inspect the changed source again before installing.", nil)
	}
	requestedMode := entry.inspection.Mode
	if requestedMode == InstallManaged {
		root, createdRoot, err = i.stageManaged(pkg, digest, materialized.root, entry.inspection.SourceID)
		if err != nil {
			return store.PluginInstallation{}, err
		}
	}

	diagnostics, _ := json.Marshal(pkg.Diagnostics)
	pluginID := entry.inspection.SourceID + "/" + pkg.ID
	now := i.now().UTC()
	record := store.PluginInstallation{ID: pluginID, Name: pkg.Name, Version: pkg.Version, Format: string(pkg.Format), Source: entry.inspection.Source, SourceID: entry.inspection.SourceID, Mode: string(requestedMode), Root: root, Digest: digest, DiagnosticsJSON: string(diagnostics), Active: true, UpdatedAt: now}
	previous, previousErr := i.st.GetPluginInstallation(ctx, pluginID)
	if previousErr == nil {
		record.InstalledAt = previous.InstalledAt
	} else if !errors.Is(previousErr, store.ErrNotFound) {
		if createdRoot {
			_ = os.RemoveAll(root)
		}
		return store.PluginInstallation{}, previousErr
	}
	if err := i.st.UpsertPluginInstallation(ctx, record); err != nil {
		if createdRoot {
			_ = os.RemoveAll(root)
		}
		return store.PluginInstallation{}, err
	}
	if projectID != "" {
		if err := i.st.SetProjectPlugin(ctx, store.ProjectPlugin{ProjectID: projectID, PluginID: pluginID, Enabled: true}); err != nil {
			i.rollbackInstallation(ctx, record, previous, previousErr == nil, createdRoot)
			return store.PluginInstallation{}, err
		}
	}
	i.mu.Lock()
	delete(i.inspections, token)
	i.mu.Unlock()
	return i.st.GetPluginInstallation(ctx, pluginID)
}

func (i *Installer) stageManaged(pkg Package, digest, sourceRoot, sourceID string) (string, bool, error) {
	segment := versionSegment(pkg.Version, digest)
	base := filepath.Join(i.home, "plugins", "cache", sourceID, pkg.ID)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", false, err
	}
	destination := filepath.Join(base, segment)
	if _, err := os.Stat(destination); err == nil {
		existingDigest, digestErr := digestTree(destination)
		if digestErr != nil {
			return "", false, digestErr
		}
		if existingDigest != digest {
			return "", false, pluginError("install", "install/version_conflict", pkg.ID, "plugin version already exists with different content", "Publish a new plugin version or remove the conflicting installation.", nil)
		}
		return destination, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	staging, err := os.MkdirTemp(base, ".staging-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(staging)
	if err := copyTreeNoLinks(sourceRoot, staging); err != nil {
		return "", false, err
	}
	stagedDigest, err := digestTree(staging)
	if err != nil {
		return "", false, err
	}
	if stagedDigest != digest {
		return "", false, pluginError("install", "install/staging_changed", pkg.ID, "staged plugin content does not match inspection", "Inspect and install the source again.", nil)
	}
	stagedPackage, err := LoadPackage(staging)
	if err != nil {
		return "", false, err
	}
	if stagedPackage.ID != pkg.ID || stagedPackage.Version != pkg.Version || stagedPackage.Format != pkg.Format {
		return "", false, pluginError("install", "install/staging_changed", pkg.ID, "staged plugin identity does not match inspection", "Inspect and install the source again.", nil)
	}
	if err := os.Rename(staging, destination); err != nil {
		return "", false, err
	}
	return destination, true, nil
}

func (i *Installer) rollbackInstallation(ctx context.Context, current, previous store.PluginInstallation, hadPrevious, createdRoot bool) {
	if hadPrevious {
		_ = i.st.UpsertPluginInstallation(ctx, previous)
	} else {
		_ = i.st.DeletePluginInstallation(ctx, current.ID)
	}
	if createdRoot {
		_ = os.RemoveAll(current.Root)
	}
}

func (i *Installer) Reload(ctx context.Context, pluginID string) (store.PluginInstallation, error) {
	previous, err := i.st.GetPluginInstallation(ctx, pluginID)
	if err != nil {
		return store.PluginInstallation{}, err
	}
	if previous.Mode != string(InstallLinked) {
		return store.PluginInstallation{}, pluginError("install", "install/reload_managed", pluginID, "only linked plugins can be reloaded", "Install in linked mode for source-folder reloads.", nil)
	}
	materialized, err := materializeSource(ctx, previous.Source)
	if err != nil {
		return store.PluginInstallation{}, err
	}
	defer materialized.cleanup()
	digest, err := digestTree(materialized.root)
	if err != nil {
		return store.PluginInstallation{}, err
	}
	pkg, err := LoadPackage(materialized.root)
	if err != nil {
		return store.PluginInstallation{}, err
	}
	if previous.SourceID+"/"+pkg.ID != pluginID {
		return store.PluginInstallation{}, pluginError("install", "install/identity_changed", pluginID, "linked plugin identity changed", "Restore the original identity or install it as a new plugin.", nil)
	}
	diagnostics, _ := json.Marshal(pkg.Diagnostics)
	updated := previous
	updated.Name, updated.Version, updated.Format, updated.Root, updated.Digest = pkg.Name, pkg.Version, string(pkg.Format), materialized.root, digest
	updated.DiagnosticsJSON, updated.UpdatedAt = string(diagnostics), i.now().UTC()
	if err := i.st.UpsertPluginInstallation(ctx, updated); err != nil {
		return store.PluginInstallation{}, err
	}
	return i.st.GetPluginInstallation(ctx, pluginID)
}

func (i *Installer) Uninstall(ctx context.Context, pluginID string, disableProjects bool) error {
	installed, err := i.st.GetPluginInstallation(ctx, pluginID)
	if err != nil {
		return err
	}
	projects, err := i.st.ProjectsUsingPlugin(ctx, pluginID)
	if err != nil {
		return err
	}
	if len(projects) > 0 && !disableProjects {
		return pluginError("uninstall", "uninstall/enabled_projects", pluginID, "plugin is enabled by one or more projects", "Disable the plugin in those projects or explicitly disable project references during uninstall.", nil)
	}
	var moved string
	if installed.Mode == string(InstallManaged) {
		cacheRoot := filepath.Join(i.home, "plugins", "cache")
		if !pathWithin(cacheRoot, installed.Root) {
			return pluginError("uninstall", "uninstall/unsafe_root", pluginID, "managed plugin root is outside the plugin cache", "Repair the installation record before uninstalling.", nil)
		}
		if _, err := os.Stat(installed.Root); err == nil {
			moved = installed.Root + ".uninstall-" + randomSuffix()
			if err := os.Rename(installed.Root, moved); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := i.st.DeletePluginInstallation(ctx, pluginID); err != nil {
		if moved != "" {
			_ = os.Rename(moved, installed.Root)
		}
		return err
	}
	if moved != "" {
		return os.RemoveAll(moved)
	}
	return nil
}

func randomToken() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func randomSuffix() string {
	var bytes [6]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func packageRequiresApproval(pkg Package) bool {
	if len(pkg.Hooks) > 0 {
		return true
	}
	for _, server := range pkg.MCPServers {
		if server.Config.Transport != "http" {
			return true
		}
	}
	return false
}

var safeVersionRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func versionSegment(version, digest string) string {
	version = strings.TrimSpace(version)
	if version != "" && len(version) <= 128 && safeVersionRE.MatchString(version) && !strings.Contains(version, "..") {
		return version
	}
	return digest[:16]
}

func pathWithin(parent, child string) bool {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(parentAbs, childAbs)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
