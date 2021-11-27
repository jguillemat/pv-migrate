package strategy

import (
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/utkuozdemir/pv-migrate/internal/k8s"
	applog "github.com/utkuozdemir/pv-migrate/internal/log"
	"github.com/utkuozdemir/pv-migrate/internal/pvc"
	"github.com/utkuozdemir/pv-migrate/internal/rsynclog"
	"github.com/utkuozdemir/pv-migrate/internal/ssh"
	"github.com/utkuozdemir/pv-migrate/internal/task"
	"io"
	"io/ioutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	portForwardTimeout   = 30 * time.Second
	sshReverseTunnelPort = 50000
)

type Local struct {
}

func (r *Local) Run(e *task.Execution) (bool, error) {
	_, err := exec.LookPath("ssh")
	if err != nil {
		return false, fmt.Errorf(":cross_mark: Error: binary not found in path: %s", "ssh")
	}

	t := e.Task
	s := t.SourceInfo
	d := t.DestInfo
	opts := t.Migration.Options

	t.Logger.Info(":key: Generating SSH key pair")
	keyAlgorithm := t.Migration.Options.KeyAlgorithm
	publicKey, privateKey, err := ssh.CreateSSHKeyPair(keyAlgorithm)
	if err != nil {
		return true, err
	}
	privateKeyMountPath := "/root/.ssh/id_" + keyAlgorithm

	srcReleaseName := e.HelmReleaseNamePrefix + "-src"
	destReleaseName := e.HelmReleaseNamePrefix + "-dest"
	releaseNames := []string{srcReleaseName, destReleaseName}

	doneCh := registerCleanupHook(e, releaseNames)
	defer cleanupAndReleaseHook(e, releaseNames, doneCh)

	srcMountPath := "/source"
	destMountPath := "/dest"

	err = installLocalOnSource(e, srcReleaseName, publicKey,
		privateKey, privateKeyMountPath, srcMountPath)
	if err != nil {
		return true, err
	}

	err = installLocalOnDest(e, destReleaseName, publicKey, destMountPath)
	if err != nil {
		return true, err
	}

	sourceSshdPod, err := getSshdPodForHelmRelease(s, srcReleaseName)
	if err != nil {
		return true, err
	}

	srcFwdPort, srcStopChan, err := portForwardForPod(t.Logger, s.ClusterClient.RestConfig,
		sourceSshdPod.Namespace, sourceSshdPod.Name)
	if err != nil {
		return true, err
	}
	defer func() { srcStopChan <- struct{}{} }()

	destSshdPod, err := getSshdPodForHelmRelease(d, destReleaseName)
	if err != nil {
		return true, err
	}

	destFwdPort, destStopChan, err := portForwardForPod(t.Logger, d.ClusterClient.RestConfig,
		destSshdPod.Namespace, destSshdPod.Name)
	if err != nil {
		return true, err
	}

	defer func() { destStopChan <- struct{}{} }()

	privateKeyFile, err := writePrivateKeyToTempFile(privateKey)
	defer func() { _ = os.Remove(privateKeyFile) }()

	if err != nil {
		return true, err
	}

	srcPath := srcMountPath + "/" + t.Migration.Source.Path
	destPath := destMountPath + "/" + t.Migration.Dest.Path

	rsyncSshArgs := fmt.Sprintf("\"ssh -p %d -o StrictHostKeyChecking=no "+
		"-o UserKnownHostsFile=/dev/null -o ConnectTimeout=5\"", sshReverseTunnelPort)
	rsyncArgs := []string{"-azv", "--info=progress2,misc0,flist0",
		"--no-inc-recursive", "-e", rsyncSshArgs}
	if opts.NoChown {
		rsyncArgs = append(rsyncArgs, "--no-o", "--no-g")
	}
	if opts.DeleteExtraneousFiles {
		rsyncArgs = append(rsyncArgs, "--delete")
	}

	rsyncCmd := fmt.Sprintf("rsync %s %s root@localhost:%s",
		strings.Join(rsyncArgs, " "), srcPath, destPath)

	cmd := exec.Command("ssh", "-i", privateKeyFile,
		"-p", strconv.Itoa(srcFwdPort),
		"-R", fmt.Sprintf("%d:localhost:%d", sshReverseTunnelPort, destFwdPort),
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "root@localhost",
		rsyncCmd,
	)

	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer

	errorCh := make(chan error)
	go func() { errorCh <- cmd.Run() }()

	showProgressBar := !e.Task.Migration.Options.NoProgressBar &&
		t.Logger.Context.Value(applog.FormatContextKey) == applog.FormatFancy
	successCh := make(chan bool, 1)

	tailConfig := rsynclog.TailConfig{
		LogReaderFunc:   func() (io.ReadCloser, error) { return reader, nil },
		SuccessCh:       successCh,
		ShowProgressBar: showProgressBar,
		Logger:          t.Logger,
	}

	go rsynclog.Tail(&tailConfig)

	err = <-errorCh
	successCh <- err == nil
	return true, err
}

func getSshdPodForHelmRelease(pvcInfo *pvc.Info, name string) (*corev1.Pod, error) {
	labelSelector := "app.kubernetes.io/component=sshd,app.kubernetes.io/instance=" + name
	return k8s.WaitForPod(pvcInfo.ClusterClient.KubeClient, pvcInfo.Claim.Namespace, labelSelector)
}

func installLocalOnSource(e *task.Execution, releaseName,
	publicKey, privateKey, privateKeyMountPath, srcMountPath string) error {
	t := e.Task
	s := t.SourceInfo
	ns := s.Claim.Namespace
	opts := t.Migration.Options

	helmValues := []string{
		"sshd.enabled=true",
		"sshd.namespace=" + ns,
		"sshd.publicKey=" + publicKey,
		"sshd.privateKeyMount=true",
		"sshd.privateKey=" + privateKey,
		"sshd.privateKeyMountPath=" + privateKeyMountPath,
		"sshd.pvcMounts[0].name=" + s.Claim.Name,
		"sshd.pvcMounts[0].readOnly=" + strconv.FormatBool(opts.SourceMountReadOnly),
		"sshd.pvcMounts[0].mountPath=" + srcMountPath,
	}

	return installHelmChart(e, s, releaseName, helmValues)
}

func installLocalOnDest(e *task.Execution, releaseName, publicKey, destMountPath string) error {
	t := e.Task
	d := t.DestInfo
	ns := d.Claim.Namespace
	helmValues := []string{
		"sshd.enabled=true",
		"sshd.namespace=" + ns,
		"sshd.publicKey=" + publicKey,
		"sshd.pvcMounts[0].name=" + d.Claim.Name,
		"sshd.pvcMounts[0].mountPath=" + destMountPath,
	}

	return installHelmChart(e, d, releaseName, helmValues)
}

func writePrivateKeyToTempFile(privateKey string) (string, error) {
	file, err := ioutil.TempFile("", "pv_migrate_private_key")
	if err != nil {
		return "", err
	}
	_, err = file.WriteString(privateKey)
	if err != nil {
		return "", err
	}

	name := file.Name()

	err = os.Chmod(name, 0600)
	if err != nil {
		return "", err
	}

	defer func() { _ = file.Close() }()
	return name, nil
}

func portForwardForPod(logger *log.Entry, restConfig *rest.Config,
	ns, name string) (int, chan<- struct{}, error) {
	port, err := getFreePort()
	if err != nil {
		return 0, nil, err
	}

	readyChan := make(chan struct{})
	stopChan := make(chan struct{})

	go func() {
		err := k8s.PortForward(&k8s.PortForwardRequest{
			RestConfig: restConfig,
			Logger:     logger,
			PodNs:      ns,
			PodName:    name,
			LocalPort:  port,
			PodPort:    22,
			StopCh:     stopChan,
			ReadyCh:    readyChan,
		})
		if err != nil {
			logger.WithError(err).WithField("ns", ns).WithField("name", name).
				WithField("port", port).Error(":cross_mark: Error on port-forward")
		}
	}()

	select {
	case <-readyChan:
		return port, stopChan, nil
	case <-time.After(portForwardTimeout):
		return 0, nil, errors.New("timed out waiting for port-forward to be ready")
	}
}

// getFreePort asks the kernel for a free open port that is ready to use.
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}
