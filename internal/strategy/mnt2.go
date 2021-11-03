package strategy

import (
	"github.com/utkuozdemir/pv-migrate/internal/k8s"
	"github.com/utkuozdemir/pv-migrate/internal/rsync"
	"github.com/utkuozdemir/pv-migrate/internal/task"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Mnt2 struct {
}

func (r *Mnt2) canDo(t *task.Task) bool {
	s := t.SourceInfo
	d := t.DestInfo
	sameCluster := s.KubeClient == d.KubeClient
	if !sameCluster {
		return false
	}

	sameNamespace := s.Claim.Namespace == d.Claim.Namespace
	if !sameNamespace {
		return false
	}

	sameNode := s.MountedNode == d.MountedNode
	return sameNode || s.SupportsROX || s.SupportsRWX || d.SupportsRWX
}

func (r *Mnt2) Run(e *task.Execution) (bool, error) {
	t := e.Task
	if !r.canDo(t) {
		return false, nil
	}

	node := determineTargetNode(t)
	migrationJob, err := buildRsyncJob(e, node)
	if err != nil {
		return true, err
	}

	doneCh := registerCleanupHook(e)
	defer cleanupAndReleaseHook(e, doneCh)
	return true, k8s.CreateJobWaitTillCompleted(e.Logger, t.SourceInfo.KubeClient,
		migrationJob, !t.Migration.Options.NoProgressBar)
}

func determineTargetNode(t *task.Task) string {
	s := t.SourceInfo
	d := t.DestInfo
	if (s.SupportsROX || s.SupportsRWX) && d.SupportsRWX {
		return ""
	}
	if !s.SupportsROX && !s.SupportsRWX {
		return s.MountedNode
	}
	return d.MountedNode
}

func buildRsyncJob(e *task.Execution, node string) (*batchv1.Job, error) {
	t := e.Task
	jobTTLSeconds := int32(600)
	backoffLimit := int32(0)
	id := e.ID
	jobName := "pv-migrate-rsync-" + id
	m := t.Migration
	opts := m.Options
	rsyncScript, err := rsync.BuildRsyncScript(opts.DeleteExtraneousFiles,
		opts.NoChown, "", m.Source.Path, m.Dest.Path)
	if err != nil {
		return nil, err
	}
	k8sJob := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: m.Dest.Namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &jobTTLSeconds,
			Template: corev1.PodTemplateSpec{

				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: m.Dest.Namespace,
					Labels:    k8s.Labels(id),
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "source-vol",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: m.Source.Name,
								},
							},
						},
						{
							Name: "dest-vol",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: m.Dest.Name,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: m.RsyncImage,
							Command: []string{
								"sh",
								"-c",
								rsyncScript,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "source-vol",
									MountPath: "/source",
								},
								{
									Name:      "dest-vol",
									MountPath: "/dest",
								},
							},
						},
					},
					NodeName:           node,
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: m.RsyncServiceAccount,
				},
			},
		},
	}
	return &k8sJob, nil
}
