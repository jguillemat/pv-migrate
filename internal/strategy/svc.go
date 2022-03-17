package strategy

import (
	"github.com/utkuozdemir/pv-migrate/internal/k8s"
	"github.com/utkuozdemir/pv-migrate/internal/rsync"
	"github.com/utkuozdemir/pv-migrate/internal/ssh"
	"github.com/utkuozdemir/pv-migrate/migration"
)

type Svc struct{}

func (r *Svc) canDo(t *migration.Migration) bool {
	s := t.SourceInfo
	d := t.DestInfo
	sameCluster := s.ClusterClient.RestConfig.Host == d.ClusterClient.RestConfig.Host

	return sameCluster
}

func (r *Svc) Run(attempt *migration.Attempt) (bool, error) {
	mig := attempt.Migration
	if !r.canDo(mig) {
		return false, nil
	}

	sourceInfo := attempt.Migration.SourceInfo
	destInfo := attempt.Migration.DestInfo
	sourceNs := sourceInfo.Claim.Namespace
	destNs := destInfo.Claim.Namespace

	mig.Logger.Info(":key: Generating SSH key pair")
	keyAlgorithm := mig.Request.KeyAlgorithm
	publicKey, privateKey, err := ssh.CreateSSHKeyPair(keyAlgorithm)
	if err != nil {
		return true, err
	}
	privateKeyMountPath := "/root/.ssh/id_" + keyAlgorithm

	releaseName := attempt.HelmReleaseNamePrefix
	releaseNames := []string{releaseName}

	sshTargetHost := releaseName + "-sshd." + sourceNs
	if mig.Request.DestHostOverride != "" {
		sshTargetHost = mig.Request.DestHostOverride
	}

	srcMountPath := "/source"
	destMountPath := "/dest"

	srcPath := srcMountPath + "/" + mig.Request.Source.Path
	destPath := destMountPath + "/" + mig.Request.Dest.Path
	rsyncCmd := rsync.Cmd{
		NoChown:    mig.Request.NoChown,
		Delete:     mig.Request.DeleteExtraneousFiles,
		SrcPath:    srcPath,
		DestPath:   destPath,
		SrcUseSSH:  true,
		SrcSSHHost: sshTargetHost,
	}
	rsyncCmdStr, err := rsyncCmd.Build()
	if err != nil {
		return true, err
	}

	vals := map[string]interface{}{
		"rsync": map[string]interface{}{
			"enabled":             true,
			"namespace":           destNs,
			"privateKeyMount":     true,
			"privateKey":          privateKey,
			"privateKeyMountPath": privateKeyMountPath,
			"pvcMounts": []map[string]interface{}{
				{
					"name":      destInfo.Claim.Name,
					"mountPath": destMountPath,
				},
			},
			"command": rsyncCmdStr,
		},
		"sshd": map[string]interface{}{
			"enabled":   true,
			"namespace": sourceNs,
			"publicKey": publicKey,
			"pvcMounts": []map[string]interface{}{
				{
					"name":      sourceInfo.Claim.Name,
					"mountPath": srcMountPath,
					"readOnly":  mig.Request.SourceMountReadOnly,
				},
			},
		},
	}

	doneCh := registerCleanupHook(attempt, releaseNames)
	defer cleanupAndReleaseHook(attempt, releaseNames, doneCh)

	err = installHelmChart(attempt, destInfo, releaseName, vals)
	if err != nil {
		return true, err
	}

	showProgressBar := !mig.Request.NoProgressBar
	kubeClient := mig.SourceInfo.ClusterClient.KubeClient
	jobName := releaseName + "-rsync"
	err = k8s.WaitForJobCompletion(attempt.Logger, kubeClient, destNs, jobName, showProgressBar)

	return true, err
}
