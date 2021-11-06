package libpod

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/containers/buildah/pkg/parse"
	"github.com/containers/common/pkg/config"
	"github.com/containers/common/pkg/secrets"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/types"
	"github.com/containers/podman/v3/libpod/define"
	"github.com/containers/podman/v3/libpod/events"
	nettypes "github.com/containers/podman/v3/libpod/network/types"
	"github.com/containers/podman/v3/pkg/namespaces"
	"github.com/containers/podman/v3/pkg/rootless"
	"github.com/containers/podman/v3/pkg/specgen"
	"github.com/containers/podman/v3/pkg/util"
	"github.com/containers/storage"
	"github.com/containers/storage/pkg/idtools"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/opencontainers/runtime-tools/generate"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// Runtime Creation Options
var (
	// SdNotifyModeValues describes the only values that SdNotifyMode can be
	SdNotifyModeValues = []string{define.SdNotifyModeContainer, define.SdNotifyModeConmon, define.SdNotifyModeIgnore}
)

// WithStorageConfig uses the given configuration to set up container storage.
// If this is not specified, the system default configuration will be used
// instead.
func WithStorageConfig(config storage.StoreOptions) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		setField := false

		if config.RunRoot != "" {
			rt.storageConfig.RunRoot = config.RunRoot
			rt.storageSet.RunRootSet = true
			setField = true
		}

		if config.GraphRoot != "" {
			rt.storageConfig.GraphRoot = config.GraphRoot
			rt.storageSet.GraphRootSet = true

			// Also set libpod static dir, so we are a subdirectory
			// of the c/storage store by default
			rt.config.Engine.StaticDir = filepath.Join(config.GraphRoot, "libpod")
			rt.storageSet.StaticDirSet = true

			// Also set libpod volume path, so we are a subdirectory
			// of the c/storage store by default
			rt.config.Engine.VolumePath = filepath.Join(config.GraphRoot, "volumes")
			rt.storageSet.VolumePathSet = true

			setField = true
		}

		graphDriverChanged := false
		if config.GraphDriverName != "" {
			rt.storageConfig.GraphDriverName = config.GraphDriverName
			rt.storageSet.GraphDriverNameSet = true
			setField = true
			graphDriverChanged = true
		}

		if config.GraphDriverOptions != nil {
			if graphDriverChanged {
				rt.storageConfig.GraphDriverOptions = make([]string, len(config.GraphDriverOptions))
				copy(rt.storageConfig.GraphDriverOptions, config.GraphDriverOptions)
			} else {
				rt.storageConfig.GraphDriverOptions = config.GraphDriverOptions
			}
			setField = true
		}

		if config.UIDMap != nil {
			rt.storageConfig.UIDMap = make([]idtools.IDMap, len(config.UIDMap))
			copy(rt.storageConfig.UIDMap, config.UIDMap)
		}

		if config.GIDMap != nil {
			rt.storageConfig.GIDMap = make([]idtools.IDMap, len(config.GIDMap))
			copy(rt.storageConfig.GIDMap, config.GIDMap)
		}

		// If any one of runroot, graphroot, graphdrivername,
		// or graphdriveroptions are set, then GraphRoot and RunRoot
		// must be set
		if setField {
			storeOpts, err := storage.DefaultStoreOptions(rootless.IsRootless(), rootless.GetRootlessUID())
			if err != nil {
				return err
			}
			if rt.storageConfig.GraphRoot == "" {
				rt.storageConfig.GraphRoot = storeOpts.GraphRoot
			}
			if rt.storageConfig.RunRoot == "" {
				rt.storageConfig.RunRoot = storeOpts.RunRoot
			}
		}

		return nil
	}
}

// WithDefaultTransport sets the default transport for retrieving images.
func WithDefaultTransport(defaultTransport string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.ImageDefaultTransport = defaultTransport

		return nil
	}
}

// WithSignaturePolicy specifies the path of a file which decides how trust is
// managed for images we've pulled.
// If this is not specified, the system default configuration will be used
// instead.
func WithSignaturePolicy(path string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.SignaturePolicyPath = path

		return nil
	}
}

// WithStateType sets the backing state implementation for libpod.
// Please note that information is not portable between backing states.
// As such, if this differs between two libpods running on the same system,
// they will not share containers, and unspecified behavior may occur.
func WithStateType(storeType config.RuntimeStateStore) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		if storeType == config.InvalidStateStore {
			return errors.Wrapf(define.ErrInvalidArg, "must provide a valid state store type")
		}

		rt.config.Engine.StateType = storeType

		return nil
	}
}

// WithOCIRuntime specifies an OCI runtime to use for running containers.
func WithOCIRuntime(runtime string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		if runtime == "" {
			return errors.Wrapf(define.ErrInvalidArg, "must provide a valid path")
		}

		rt.config.Engine.OCIRuntime = runtime

		return nil
	}
}

// WithConmonPath specifies the path to the conmon binary which manages the
// runtime.
func WithConmonPath(path string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		if path == "" {
			return errors.Wrapf(define.ErrInvalidArg, "must provide a valid path")
		}

		rt.config.Engine.ConmonPath = []string{path}

		return nil
	}
}

// WithConmonEnv specifies the environment variable list for the conmon process.
func WithConmonEnv(environment []string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.ConmonEnvVars = make([]string, len(environment))
		copy(rt.config.Engine.ConmonEnvVars, environment)

		return nil
	}
}

// WithNetworkCmdPath specifies the path to the slirp4netns binary which manages the
// runtime.
func WithNetworkCmdPath(path string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.NetworkCmdPath = path

		return nil
	}
}

// WithCgroupManager specifies the manager implementation name which is used to
// handle cgroups for containers.
// Current valid values are "cgroupfs" and "systemd".
func WithCgroupManager(manager string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		if manager != config.CgroupfsCgroupsManager && manager != config.SystemdCgroupsManager {
			return errors.Wrapf(define.ErrInvalidArg, "CGroup manager must be one of %s and %s",
				config.CgroupfsCgroupsManager, config.SystemdCgroupsManager)
		}

		rt.config.Engine.CgroupManager = manager

		return nil
	}
}

// WithStaticDir sets the directory that static runtime files which persist
// across reboots will be stored.
func WithStaticDir(dir string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.StaticDir = dir
		rt.config.Engine.StaticDirSet = true

		return nil
	}
}

// WithRegistriesConf configures the runtime to always use specified
// registries.conf for image processing.
func WithRegistriesConf(path string) RuntimeOption {
	logrus.Debugf("Setting custom registries.conf: %q", path)
	return func(rt *Runtime) error {
		if _, err := os.Stat(path); err != nil {
			return errors.Wrap(err, "locating specified registries.conf")
		}
		if rt.imageContext == nil {
			rt.imageContext = &types.SystemContext{
				BigFilesTemporaryDir: parse.GetTempDir(),
			}
		}

		rt.imageContext.SystemRegistriesConfPath = path
		return nil
	}
}

// WithHooksDir sets the directories to look for OCI runtime hook configuration.
func WithHooksDir(hooksDirs ...string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		for _, hooksDir := range hooksDirs {
			if hooksDir == "" {
				return errors.Wrap(define.ErrInvalidArg, "empty-string hook directories are not supported")
			}
		}

		rt.config.Engine.HooksDir = hooksDirs
		return nil
	}
}

// WithCDI sets the devices to check for for CDI configuration.
func WithCDI(devices []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.CDIDevices = devices
		return nil
	}
}

// WithStorageOpts sets the devices to check for for CDI configuration.
func WithStorageOpts(storageOpts map[string]string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.StorageOpts = storageOpts
		return nil
	}
}

// WithDefaultMountsFile sets the file to look at for default mounts (mainly
// secrets).
// Note we are not saving this in the database as it is for testing purposes
// only.
func WithDefaultMountsFile(mountsFile string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		if mountsFile == "" {
			return define.ErrInvalidArg
		}
		rt.config.Containers.DefaultMountsFile = mountsFile
		return nil
	}
}

// WithTmpDir sets the directory that temporary runtime files which are not
// expected to survive across reboots will be stored.
// This should be located on a tmpfs mount (/tmp or /run for example).
func WithTmpDir(dir string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}
		rt.config.Engine.TmpDir = dir
		rt.config.Engine.TmpDirSet = true

		return nil
	}
}

// WithNoStore sets a bool on the runtime that we do not need
// any containers storage.
func WithNoStore() RuntimeOption {
	return func(rt *Runtime) error {
		rt.noStore = true
		return nil
	}
}

// WithNoPivotRoot sets the runtime to use MS_MOVE instead of PIVOT_ROOT when
// starting containers.
func WithNoPivotRoot() RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.NoPivotRoot = true

		return nil
	}
}

// WithCNIConfigDir sets the CNI configuration directory.
func WithCNIConfigDir(dir string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Network.NetworkConfigDir = dir

		return nil
	}
}

// WithCNIPluginDir sets the CNI plugins directory.
func WithCNIPluginDir(dir string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Network.CNIPluginDirs = []string{dir}

		return nil
	}
}

// WithNamespace sets the namespace for libpod.
// Namespaces are used to create scopes to separate containers and pods
// in the state.
// When namespace is set, libpod will only view containers and pods in
// the same namespace. All containers and pods created will default to
// the namespace set here.
// A namespace of "", the empty string, is equivalent to no namespace,
// and all containers and pods will be visible.
func WithNamespace(ns string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.Namespace = ns

		return nil
	}
}

// WithVolumePath sets the path under which all named volumes
// should be created.
// The path changes based on whether the user is running as root or not.
func WithVolumePath(volPath string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.VolumePath = volPath
		rt.config.Engine.VolumePathSet = true

		return nil
	}
}

// WithDefaultInfraImage sets the infra image for libpod.
// An infra image is used for inter-container kernel
// namespace sharing within a pod. Typically, an infra
// container is lightweight and is there to reap
// zombie processes within its pid namespace.
func WithDefaultInfraImage(img string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.InfraImage = img

		return nil
	}
}

// WithDefaultInfraCommand sets the command to
// run on pause container start up.
func WithDefaultInfraCommand(cmd string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.InfraCommand = cmd

		return nil
	}
}

// WithDefaultInfraName sets the infra container name for a single pod.
func WithDefaultInfraName(name string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.config.Engine.InfraImage = name

		return nil
	}
}

// WithRenumber instructs libpod to perform a lock renumbering while
// initializing. This will handle migrations from early versions of libpod with
// file locks to newer versions with SHM locking, as well as changes in the
// number of configured locks.
func WithRenumber() RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.doRenumber = true

		return nil
	}
}

// WithMigrate instructs libpod to migrate container configurations to account
// for changes between Engine versions. All running containers will be stopped
// during a migration, then restarted after the migration is complete.
func WithMigrate() RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		rt.doMigrate = true

		return nil
	}
}

// WithMigrateRuntime instructs Engine to change the default OCI runtime on all
// containers during a migration. This is not used if `MigrateRuntime()` is not
// also passed.
// Engine makes no promises that your containers continue to work with the new
// runtime - migrations between dissimilar runtimes may well break things.
// Use with caution.
func WithMigrateRuntime(requestedRuntime string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		if requestedRuntime == "" {
			return errors.Wrapf(define.ErrInvalidArg, "must provide a non-empty name for new runtime")
		}

		rt.migrateRuntime = requestedRuntime

		return nil
	}
}

// WithEventsLogger sets the events backend to use.
// Currently supported values are "file" for file backend and "journald" for
// journald backend.
func WithEventsLogger(logger string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}

		if !events.IsValidEventer(logger) {
			return errors.Wrapf(define.ErrInvalidArg, "%q is not a valid events backend", logger)
		}

		rt.config.Engine.EventsLogger = logger
		rt.config.Engine.EventsLogFilePath = filepath.Join(rt.config.Engine.TmpDir, "events", "events.log")

		return nil
	}
}

// WithEnableSDNotify sets a runtime option so we know whether to disable socket/FD
// listening
func WithEnableSDNotify() RuntimeOption {
	return func(rt *Runtime) error {
		rt.config.Engine.SDNotify = true
		return nil
	}
}

// WithRuntimeFlags adds the global runtime flags to the container config
func WithRuntimeFlags(runtimeFlags []string) RuntimeOption {
	return func(rt *Runtime) error {
		if rt.valid {
			return define.ErrRuntimeFinalized
		}
		rt.runtimeFlags = runtimeFlags
		return nil
	}
}

// Container Creation Options

// WithMaxLogSize sets the maximum size of container logs.
// Positive sizes are limits in bytes, -1 is unlimited.
func WithMaxLogSize(limit int64) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrRuntimeFinalized
		}
		ctr.config.LogSize = limit

		return nil
	}
}

// WithShmDir sets the directory that should be mounted on /dev/shm.
func WithShmDir(dir string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.ShmDir = dir
		return nil
	}
}

// WithSystemd turns on systemd mode in the container
func WithSystemd() CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Systemd = true
		return nil
	}
}

// WithSdNotifyMode sets the sd-notify method
func WithSdNotifyMode(mode string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		// verify values
		if len(mode) > 0 && !util.StringInSlice(strings.ToLower(mode), SdNotifyModeValues) {
			return errors.Wrapf(define.ErrInvalidArg, "--sdnotify values must be one of %q", strings.Join(SdNotifyModeValues, ", "))
		}

		ctr.config.SdNotifyMode = mode
		return nil
	}
}

// WithShmSize sets the size of /dev/shm tmpfs mount.
func WithShmSize(size int64) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.ShmSize = size
		return nil
	}
}

// WithPrivileged sets the privileged flag in the container runtime.
func WithPrivileged(privileged bool) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Privileged = privileged
		return nil
	}
}

// WithSecLabels sets the labels for SELinux.
func WithSecLabels(labelOpts []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.LabelOpts = labelOpts
		return nil
	}
}

// WithUser sets the user identity field in configuration.
// Valid uses [user | user:group | uid | uid:gid | user:gid | uid:group ].
func WithUser(user string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.User = user
		return nil
	}
}

// WithRootFSFromImage sets up a fresh root filesystem using the given image.
// If useImageConfig is specified, image volumes, environment variables, and
// other configuration from the image will be added to the config.
// TODO: Replace image name and ID with a libpod.Image struct when that is
// finished.
func WithRootFSFromImage(imageID, imageName, rawImageName string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.RootfsImageID = imageID
		ctr.config.RootfsImageName = imageName
		ctr.config.RawImageName = rawImageName
		return nil
	}
}

// WithStdin keeps stdin on the container open to allow interaction.
func WithStdin() CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Stdin = true

		return nil
	}
}

// WithPod adds the container to a pod.
// Containers which join a pod can only join the Linux namespaces of other
// containers in the same pod.
// Containers can only join pods in the same libpod namespace.
func (r *Runtime) WithPod(pod *Pod) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if pod == nil {
			return define.ErrInvalidArg
		}
		ctr.config.Pod = pod.ID()

		return nil
	}
}

// WithLabels adds labels to the container.
func WithLabels(labels map[string]string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Labels = make(map[string]string)
		for key, value := range labels {
			ctr.config.Labels[key] = value
		}

		return nil
	}
}

// WithName sets the container's name.
func WithName(name string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		// Check the name against a regex
		if !define.NameRegex.MatchString(name) {
			return define.RegexError
		}

		ctr.config.Name = name

		return nil
	}
}

// WithStopSignal sets the signal that will be sent to stop the container.
func WithStopSignal(signal syscall.Signal) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if signal == 0 {
			return errors.Wrapf(define.ErrInvalidArg, "stop signal cannot be 0")
		} else if signal > 64 {
			return errors.Wrapf(define.ErrInvalidArg, "stop signal cannot be greater than 64 (SIGRTMAX)")
		}

		ctr.config.StopSignal = uint(signal)

		return nil
	}
}

// WithStopTimeout sets the time to after initial stop signal is sent to the
// container, before sending the kill signal.
func WithStopTimeout(timeout uint) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.StopTimeout = timeout

		return nil
	}
}

// WithTimeout sets the maximum time a container is allowed to run"
func WithTimeout(timeout uint) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Timeout = timeout

		return nil
	}
}

// WithIDMappings sets the idmappings for the container
func WithIDMappings(idmappings storage.IDMappingOptions) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.IDMappings = idmappings
		return nil
	}
}

// WithExitCommand sets the ExitCommand for the container, appending on the ctr.ID() to the end
func WithExitCommand(exitCommand []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.ExitCommand = exitCommand
		ctr.config.ExitCommand = append(ctr.config.ExitCommand, ctr.ID())

		return nil
	}
}

// WithUTSNSFromPod indicates the the container should join the UTS namespace of
// its pod
func WithUTSNSFromPod(p *Pod) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if err := validPodNSOption(p, ctr.config.Pod); err != nil {
			return err
		}

		infraContainer, err := p.InfraContainerID()
		if err != nil {
			return err
		}
		ctr.config.UTSNsCtr = infraContainer

		return nil
	}
}

// WithIPCNSFrom indicates the the container should join the IPC namespace of
// the given container.
// If the container has joined a pod, it can only join the namespaces of
// containers in the same pod.
func WithIPCNSFrom(nsCtr *Container) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if err := checkDependencyContainer(nsCtr, ctr); err != nil {
			return err
		}

		ctr.config.IPCNsCtr = nsCtr.ID()

		return nil
	}
}

// WithMountNSFrom indicates the the container should join the mount namespace
// of the given container.
// If the container has joined a pod, it can only join the namespaces of
// containers in the same pod.
func WithMountNSFrom(nsCtr *Container) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if err := checkDependencyContainer(nsCtr, ctr); err != nil {
			return err
		}
		ctr.config.MountNsCtr = nsCtr.ID()

		return nil
	}
}

// WithNetNSFrom indicates the the container should join the network namespace
// of the given container.
// If the container has joined a pod, it can only join the namespaces of
// containers in the same pod.
func WithNetNSFrom(nsCtr *Container) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if err := checkDependencyContainer(nsCtr, ctr); err != nil {
			return err
		}

		ctr.config.NetNsCtr = nsCtr.ID()

		return nil
	}
}

// WithPIDNSFrom indicates the the container should join the PID namespace of
// the given container.
// If the container has joined a pod, it can only join the namespaces of
// containers in the same pod.
func WithPIDNSFrom(nsCtr *Container) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if err := checkDependencyContainer(nsCtr, ctr); err != nil {
			return err
		}

		ctr.config.PIDNsCtr = nsCtr.ID()

		return nil
	}
}

// WithAddCurrentUserPasswdEntry indicates that container should add current
// user entry to /etc/passwd, since the UID will be mapped into the container,
// via user namespace
func WithAddCurrentUserPasswdEntry() CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.AddCurrentUserPasswdEntry = true
		return nil
	}
}

// WithUserNSFrom indicates the the container should join the user namespace of
// the given container.
// If the container has joined a pod, it can only join the namespaces of
// containers in the same pod.
func WithUserNSFrom(nsCtr *Container) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if err := checkDependencyContainer(nsCtr, ctr); err != nil {
			return err
		}

		ctr.config.UserNsCtr = nsCtr.ID()
		if err := JSONDeepCopy(nsCtr.IDMappings(), &ctr.config.IDMappings); err != nil {
			return err
		}
		g := generate.NewFromSpec(ctr.config.Spec)

		g.ClearLinuxUIDMappings()
		for _, uidmap := range nsCtr.config.IDMappings.UIDMap {
			g.AddLinuxUIDMapping(uint32(uidmap.HostID), uint32(uidmap.ContainerID), uint32(uidmap.Size))
		}
		g.ClearLinuxGIDMappings()
		for _, gidmap := range nsCtr.config.IDMappings.GIDMap {
			g.AddLinuxGIDMapping(uint32(gidmap.HostID), uint32(gidmap.ContainerID), uint32(gidmap.Size))
		}
		return nil
	}
}

// WithUTSNSFrom indicates the the container should join the UTS namespace of
// the given container.
// If the container has joined a pod, it can only join the namespaces of
// containers in the same pod.
func WithUTSNSFrom(nsCtr *Container) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if err := checkDependencyContainer(nsCtr, ctr); err != nil {
			return err
		}

		ctr.config.UTSNsCtr = nsCtr.ID()

		return nil
	}
}

// WithCgroupNSFrom indicates the the container should join the CGroup namespace
// of the given container.
// If the container has joined a pod, it can only join the namespaces of
// containers in the same pod.
func WithCgroupNSFrom(nsCtr *Container) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if err := checkDependencyContainer(nsCtr, ctr); err != nil {
			return err
		}

		ctr.config.CgroupNsCtr = nsCtr.ID()

		return nil
	}
}

// WithDependencyCtrs sets dependency containers of the given container.
// Dependency containers must be running before this container is started.
func WithDependencyCtrs(ctrs []*Container) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		deps := make([]string, 0, len(ctrs))

		for _, dep := range ctrs {
			if err := checkDependencyContainer(dep, ctr); err != nil {
				return err
			}

			deps = append(deps, dep.ID())
		}

		ctr.config.Dependencies = deps

		return nil
	}
}

// WithNetNS indicates that the container should be given a new network
// namespace with a minimal configuration.
// An optional array of port mappings can be provided.
// Conflicts with WithNetNSFrom().
func WithNetNS(portMappings []nettypes.PortMapping, exposedPorts map[uint16][]string, postConfigureNetNS bool, netmode string, networks []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.PostConfigureNetNS = postConfigureNetNS
		ctr.config.NetMode = namespaces.NetworkMode(netmode)
		ctr.config.CreateNetNS = true
		ctr.config.PortMappings = portMappings
		ctr.config.ExposedPorts = exposedPorts

		ctr.config.Networks = networks

		return nil
	}
}

// WithStaticIP indicates that the container should request a static IP from
// the CNI plugins.
// It cannot be set unless WithNetNS has already been passed.
// Further, it cannot be set if additional CNI networks to join have been
// specified.
func WithStaticIP(ip net.IP) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.StaticIP = ip

		return nil
	}
}

// WithNetworkOptions sets additional options for the networks.
func WithNetworkOptions(options map[string][]string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.NetworkOptions = options

		return nil
	}
}

// WithStaticMAC indicates that the container should request a static MAC from
// the CNI plugins.
// It cannot be set unless WithNetNS has already been passed.
// Further, it cannot be set if additional CNI networks to join have been
// specified.
func WithStaticMAC(mac nettypes.HardwareAddr) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.StaticMAC = mac

		return nil
	}
}

// WithLogDriver sets the log driver for the container
func WithLogDriver(driver string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		switch driver {
		case "":
			return errors.Wrapf(define.ErrInvalidArg, "log driver must be set")
		case define.JournaldLogging, define.KubernetesLogging, define.JSONLogging, define.NoLogging, define.PassthroughLogging:
			break
		default:
			return errors.Wrapf(define.ErrInvalidArg, "invalid log driver")
		}

		ctr.config.LogDriver = driver

		return nil
	}
}

// WithLogPath sets the path to the log file.
func WithLogPath(path string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		if path == "" {
			return errors.Wrapf(define.ErrInvalidArg, "log path must be set")
		}

		ctr.config.LogPath = path

		return nil
	}
}

// WithLogTag sets the tag to the log file.
func WithLogTag(tag string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		if tag == "" {
			return errors.Wrapf(define.ErrInvalidArg, "log tag must be set")
		}

		ctr.config.LogTag = tag

		return nil
	}
}

// WithCgroupsMode disables the creation of CGroups for the conmon process.
func WithCgroupsMode(mode string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		switch mode {
		case "disabled":
			ctr.config.NoCgroups = true
			ctr.config.CgroupsMode = mode
		case "enabled", "no-conmon", cgroupSplit:
			ctr.config.CgroupsMode = mode
		default:
			return errors.Wrapf(define.ErrInvalidArg, "Invalid cgroup mode %q", mode)
		}

		return nil
	}
}

// WithCgroupParent sets the Cgroup Parent of the new container.
func WithCgroupParent(parent string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if parent == "" {
			return errors.Wrapf(define.ErrInvalidArg, "cgroup parent cannot be empty")
		}

		ctr.config.CgroupParent = parent

		return nil
	}
}

// WithDNSSearch sets the additional search domains of a container.
func WithDNSSearch(searchDomains []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.DNSSearch = searchDomains
		return nil
	}
}

// WithDNS sets additional name servers for the container.
func WithDNS(dnsServers []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		var dns []net.IP
		for _, i := range dnsServers {
			result := net.ParseIP(i)
			if result == nil {
				return errors.Wrapf(define.ErrInvalidArg, "invalid IP address %s", i)
			}
			dns = append(dns, result)
		}
		ctr.config.DNSServer = append(ctr.config.DNSServer, dns...)

		return nil
	}
}

// WithDNSOption sets addition dns options for the container.
func WithDNSOption(dnsOptions []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		if ctr.config.UseImageResolvConf {
			return errors.Wrapf(define.ErrInvalidArg, "cannot add DNS options if container will not create /etc/resolv.conf")
		}
		ctr.config.DNSOption = append(ctr.config.DNSOption, dnsOptions...)
		return nil
	}
}

// WithHosts sets additional host:IP for the hosts file.
func WithHosts(hosts []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.HostAdd = hosts
		return nil
	}
}

// WithConmonPidFile specifies the path to the file that receives the pid of
// conmon.
func WithConmonPidFile(path string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.ConmonPidFile = path
		return nil
	}
}

// WithGroups sets additional groups for the container, which are defined by
// the user.
func WithGroups(groups []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.Groups = groups
		return nil
	}
}

// WithUserVolumes sets the user-added volumes of the container.
// These are not added to the container's spec, but will instead be used during
// commit to populate the volumes of the new image, and to trigger some OCI
// hooks that are only added if volume mounts are present.
// Furthermore, they are used in the output of inspect, to filter volumes -
// only volumes included in this list will be included in the output.
// Unless explicitly set, committed images will have no volumes.
// The given volumes slice must not be nil.
func WithUserVolumes(volumes []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		if volumes == nil {
			return define.ErrInvalidArg
		}

		ctr.config.UserVolumes = make([]string, 0, len(volumes))
		ctr.config.UserVolumes = append(ctr.config.UserVolumes, volumes...)
		return nil
	}
}

// WithEntrypoint sets the entrypoint of the container.
// This is not used to change the container's spec, but will instead be used
// during commit to populate the entrypoint of the new image.
// If not explicitly set it will default to the image's entrypoint.
// A nil entrypoint is allowed, and will clear entrypoint on the created image.
func WithEntrypoint(entrypoint []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Entrypoint = make([]string, 0, len(entrypoint))
		ctr.config.Entrypoint = append(ctr.config.Entrypoint, entrypoint...)
		return nil
	}
}

// WithCommand sets the command of the container.
// This is not used to change the container's spec, but will instead be used
// during commit to populate the command of the new image.
// If not explicitly set it will default to the image's command.
// A nil command is allowed, and will clear command on the created image.
func WithCommand(command []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Command = make([]string, 0, len(command))
		ctr.config.Command = append(ctr.config.Command, command...)
		return nil
	}
}

// WithRootFS sets the rootfs for the container.
// This creates a container from a directory on disk and not an image.
func WithRootFS(rootfs string, overlay bool) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		if _, err := os.Stat(rootfs); err != nil {
			return err
		}
		ctr.config.Rootfs = rootfs
		ctr.config.RootfsOverlay = overlay
		return nil
	}
}

// WithCtrNamespace sets the namespace the container will be created in.
// Namespaces are used to create separate views of Podman's state - runtimes can
// join a specific namespace and see only containers and pods in that namespace.
// Empty string namespaces are allowed, and correspond to a lack of namespace.
func WithCtrNamespace(ns string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Namespace = ns

		return nil
	}
}

// WithUseImageResolvConf tells the container not to bind-mount resolv.conf in.
// This conflicts with other DNS-related options.
func WithUseImageResolvConf() CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.UseImageResolvConf = true

		return nil
	}
}

// WithUseImageHosts tells the container not to bind-mount /etc/hosts in.
// This conflicts with WithHosts().
func WithUseImageHosts() CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.UseImageHosts = true

		return nil
	}
}

// WithRestartPolicy sets the container's restart policy. Valid values are
// "no", "on-failure", and "always". The empty string is allowed, and will be
// equivalent to "no".
func WithRestartPolicy(policy string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		switch policy {
		case define.RestartPolicyNone, define.RestartPolicyNo, define.RestartPolicyOnFailure, define.RestartPolicyAlways, define.RestartPolicyUnlessStopped:
			ctr.config.RestartPolicy = policy
		default:
			return errors.Wrapf(define.ErrInvalidArg, "%q is not a valid restart policy", policy)
		}

		return nil
	}
}

// WithRestartRetries sets the number of retries to use when restarting a
// container with the "on-failure" restart policy.
// 0 is an allowed value, and indicates infinite retries.
func WithRestartRetries(tries uint) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.RestartRetries = tries

		return nil
	}
}

// WithNamedVolumes adds the given named volumes to the container.
func WithNamedVolumes(volumes []*ContainerNamedVolume) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		for _, vol := range volumes {
			mountOpts, err := util.ProcessOptions(vol.Options, false, "")
			if err != nil {
				return errors.Wrapf(err, "processing options for named volume %q mounted at %q", vol.Name, vol.Dest)
			}

			ctr.config.NamedVolumes = append(ctr.config.NamedVolumes, &ContainerNamedVolume{
				Name:    vol.Name,
				Dest:    vol.Dest,
				Options: mountOpts,
			})
		}

		return nil
	}
}

// WithOverlayVolumes adds the given overlay volumes to the container.
func WithOverlayVolumes(volumes []*ContainerOverlayVolume) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		for _, vol := range volumes {
			ctr.config.OverlayVolumes = append(ctr.config.OverlayVolumes, &ContainerOverlayVolume{
				Dest:    vol.Dest,
				Source:  vol.Source,
				Options: vol.Options,
			})
		}

		return nil
	}
}

// WithImageVolumes adds the given image volumes to the container.
func WithImageVolumes(volumes []*ContainerImageVolume) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		for _, vol := range volumes {
			ctr.config.ImageVolumes = append(ctr.config.ImageVolumes, &ContainerImageVolume{
				Dest:      vol.Dest,
				Source:    vol.Source,
				ReadWrite: vol.ReadWrite,
			})
		}

		return nil
	}
}

// WithHealthCheck adds the healthcheck to the container config
func WithHealthCheck(healthCheck *manifest.Schema2HealthConfig) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.HealthCheckConfig = healthCheck
		return nil
	}
}

// WithPreserveFDs forwards from the process running Libpod into the container
// the given number of extra FDs (starting after the standard streams) to the created container
func WithPreserveFDs(fd uint) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.PreserveFDs = fd
		return nil
	}
}

// WithCreateCommand adds the full command plus arguments of the current
// process to the container config.
func WithCreateCommand(cmd []string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.CreateCommand = cmd
		return nil
	}
}

// withIsInfra allows us to dfferentiate between infra containers and regular containers
// within the container config
func withIsInfra() CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.IsInfra = true

		return nil
	}
}

// WithCreateWorkingDir tells Podman to create the container's working directory
// if it does not exist.
func WithCreateWorkingDir() CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.CreateWorkingDir = true
		return nil
	}
}

// WithNetworkAliases sets network aliases for the container.
// Accepts a map of network name to aliases.
func WithNetworkAliases(aliases map[string][]string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.NetworkAliases = aliases

		return nil
	}
}

// Volume Creation Options

// WithVolumeName sets the name of the volume.
func WithVolumeName(name string) VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		// Check the name against a regex
		if !define.NameRegex.MatchString(name) {
			return define.RegexError
		}
		volume.config.Name = name

		return nil
	}
}

// WithVolumeDriver sets the volume's driver.
// It is presently not implemented, but will be supported in a future Podman
// release.
func WithVolumeDriver(driver string) VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.config.Driver = driver
		return nil
	}
}

// WithVolumeLabels sets the labels of the volume.
func WithVolumeLabels(labels map[string]string) VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.config.Labels = make(map[string]string)
		for key, value := range labels {
			volume.config.Labels[key] = value
		}

		return nil
	}
}

// WithVolumeOptions sets the options of the volume.
func WithVolumeOptions(options map[string]string) VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.config.Options = make(map[string]string)
		for key, value := range options {
			volume.config.Options[key] = value
		}

		return nil
	}
}

// WithVolumeUID sets the UID that the volume will be created as.
func WithVolumeUID(uid int) VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.config.UID = uid

		return nil
	}
}

// WithVolumeSize sets the maximum size of the volume
func WithVolumeSize(size uint64) VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.config.Size = size

		return nil
	}
}

// WithVolumeInodes sets the maximum inodes of the volume
func WithVolumeInodes(inodes uint64) VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.config.Inodes = inodes

		return nil
	}
}

// WithVolumeGID sets the GID that the volume will be created as.
func WithVolumeGID(gid int) VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.config.GID = gid

		return nil
	}
}

// WithVolumeNoChown prevents the volume from being chowned to the process uid at first use.
func WithVolumeNoChown() VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.state.NeedsChown = false

		return nil
	}
}

// withSetAnon sets a bool notifying libpod that this volume is anonymous and
// should be removed when containers using it are removed and volumes are
// specified for removal.
func withSetAnon() VolumeCreateOption {
	return func(volume *Volume) error {
		if volume.valid {
			return define.ErrVolumeFinalized
		}

		volume.config.IsAnon = true

		return nil
	}
}

// WithTimezone sets the timezone in the container
func WithTimezone(path string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		if path != "local" {
			zone := filepath.Join("/usr/share/zoneinfo", path)

			file, err := os.Stat(zone)
			if err != nil {
				return err
			}
			//We don't want to mount a timezone directory
			if file.IsDir() {
				return errors.New("Invalid timezone: is a directory")
			}
		}

		ctr.config.Timezone = path
		return nil
	}
}

// WithUmask sets the umask in the container
func WithUmask(umask string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		if !define.UmaskRegex.MatchString(umask) {
			return errors.Wrapf(define.ErrInvalidArg, "Invalid umask string %s", umask)
		}
		ctr.config.Umask = umask
		return nil
	}
}

// WithSecrets adds secrets to the container
func WithSecrets(containerSecrets []*ContainerSecret) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.Secrets = containerSecrets
		return nil
	}
}

// WithSecrets adds environment variable secrets to the container
func WithEnvSecrets(envSecrets map[string]string) CtrCreateOption {
	return func(ctr *Container) error {
		ctr.config.EnvSecrets = make(map[string]*secrets.Secret)
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		manager, err := ctr.runtime.SecretsManager()
		if err != nil {
			return err
		}
		for target, src := range envSecrets {
			secr, err := manager.Lookup(src)
			if err != nil {
				return err
			}
			ctr.config.EnvSecrets[target] = secr
		}
		return nil
	}
}

// WithPidFile adds pidFile to the container
func WithPidFile(pidFile string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.PidFile = pidFile
		return nil
	}
}

// WithInitCtrType indicates the container is a initcontainer
func WithInitCtrType(containerType string) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		// Make sure the type is valid
		if containerType == define.OneShotInitContainer || containerType == define.AlwaysInitContainer {
			ctr.config.InitContainerType = containerType
			return nil
		}
		return errors.Errorf("%s is invalid init container type", containerType)
	}
}

// WithHostDevice adds the original host src to the config
func WithHostDevice(dev []specs.LinuxDevice) CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}
		ctr.config.DeviceHostSrc = dev
		return nil
	}
}

// Pod Creation Options

// WithPodCreateCommand adds the full command plus arguments of the current
// process to the pod config.
func WithPodCreateCommand(createCmd []string) PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}
		pod.config.CreateCommand = createCmd
		return nil
	}
}

// WithPodName sets the name of the pod.
func WithPodName(name string) PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		// Check the name against a regex
		if !define.NameRegex.MatchString(name) {
			return define.RegexError
		}

		pod.config.Name = name

		return nil
	}
}

// WithPodHostname sets the hostname of the pod.
func WithPodHostname(hostname string) PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		// Check the hostname against a regex
		if !define.NameRegex.MatchString(hostname) {
			return define.RegexError
		}

		pod.config.Hostname = hostname

		return nil
	}
}

// WithInfraConmonPidFile sets the path to a custom conmon PID file for the
// infra container.
func WithInfraConmonPidFile(path string, infraSpec *specgen.SpecGenerator) PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}
		infraSpec.ConmonPidFile = path
		return nil
	}
}

// WithPodLabels sets the labels of a pod.
func WithPodLabels(labels map[string]string) PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.Labels = make(map[string]string)
		for key, value := range labels {
			pod.config.Labels[key] = value
		}

		return nil
	}
}

// WithPodCgroupParent sets the Cgroup Parent of the pod.
func WithPodCgroupParent(path string) PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.CgroupParent = path

		return nil
	}
}

// WithPodCgroups tells containers in this pod to use the cgroup created for
// this pod.
// This can still be overridden at the container level by explicitly specifying
// a CGroup parent.
func WithPodCgroups() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.UsePodCgroup = true

		return nil
	}
}

// WithPodNamespace sets the namespace for the created pod.
// Namespaces are used to create separate views of Podman's state - runtimes can
// join a specific namespace and see only containers and pods in that namespace.
// Empty string namespaces are allowed, and correspond to a lack of namespace.
// Containers must belong to the same namespace as the pod they join.
func WithPodNamespace(ns string) PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.Namespace = ns

		return nil
	}
}

// WithPodIPC tells containers in this pod to use the ipc namespace
// created for this pod.
// Containers in a pod will inherit the kernel namespaces from the
// first container added.
func WithPodIPC() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.UsePodIPC = true

		return nil
	}
}

// WithPodNet tells containers in this pod to use the network namespace
// created for this pod.
// Containers in a pod will inherit the kernel namespaces from the
// first container added.
func WithPodNet() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.UsePodNet = true

		return nil
	}
}

// WithPodMount tells containers in this pod to use the mount namespace
// created for this pod.
// Containers in a pod will inherit the kernel namespaces from the
// first container added.
// TODO implement WithMountNSFrom, so WithMountNsFromPod functions properly
// Then this option can be added on the pod level
func WithPodMount() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.UsePodMount = true

		return nil
	}
}

// WithPodUser tells containers in this pod to use the user namespace
// created for this pod.
// Containers in a pod will inherit the kernel namespaces from the
// first container added.
// TODO implement WithUserNSFrom, so WithUserNsFromPod functions properly
// Then this option can be added on the pod level
func WithPodUser() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.UsePodUser = true

		return nil
	}
}

// WithPodPID tells containers in this pod to use the pid namespace
// created for this pod.
// Containers in a pod will inherit the kernel namespaces from the
// first container added.
func WithPodPID() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.UsePodPID = true

		return nil
	}
}

// WithPodUTS tells containers in this pod to use the uts namespace
// created for this pod.
// Containers in a pod will inherit the kernel namespaces from the
// first container added.
func WithPodUTS() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.UsePodUTS = true

		return nil
	}
}

// WithPodCgroup tells containers in this pod to use the cgroup namespace
// created for this pod.
// Containers in a pod will inherit the kernel namespaces from the first
// container added.
func WithPodCgroup() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}

		pod.config.UsePodCgroupNS = true

		return nil
	}
}

// WithInfraContainer tells the pod to create a pause container
func WithInfraContainer() PodCreateOption {
	return func(pod *Pod) error {
		if pod.valid {
			return define.ErrPodFinalized
		}
		pod.config.HasInfra = true

		return nil
	}
}

// WithVolatile sets the volatile flag for the container storage.
// The option can potentially cause data loss when used on a container that must survive a machine reboot.
func WithVolatile() CtrCreateOption {
	return func(ctr *Container) error {
		if ctr.valid {
			return define.ErrCtrFinalized
		}

		ctr.config.Volatile = true

		return nil
	}
}
