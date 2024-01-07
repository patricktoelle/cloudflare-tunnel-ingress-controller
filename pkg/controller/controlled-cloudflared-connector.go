package controller

import (
	"context"
	"os"
	"strconv"

	cloudflarecontroller "github.com/STRRL/cloudflare-tunnel-ingress-controller/pkg/cloudflare-controller"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CaPool struct {
	ConfigMap *struct{ Name string }
	Secret    *struct{ Name string }
	Key       string
	MountPath string
}

func CreateControlledCloudflaredIfNotExist(
	ctx context.Context,
	kubeClient client.Client,
	tunnelClient *cloudflarecontroller.TunnelClient,
	namespace string,
	caPool *CaPool,
) error {
	list := appsv1.DeploymentList{}
	err := kubeClient.List(ctx, &list, &client.ListOptions{
		Namespace: namespace,
		LabelSelector: labels.SelectorFromSet(labels.Set{
			"strrl.dev/cloudflare-tunnel-ingress-controller": "controlled-cloudflared-connector",
		}),
	})
	if err != nil {
		return errors.Wrapf(err, "list controlled-cloudflared-connector in namespace %s", namespace)
	}

	if len(list.Items) > 0 {
		return nil
	}

	var caPoolSource *v1.VolumeSource = nil
	if caPool.ConfigMap != nil && caPool.Secret != nil {
		return errors.New("Only one of --capool-config-map or --capool-secret may be specified")
	} else if caPool.ConfigMap != nil {
		caPoolSource = &v1.VolumeSource{
			ConfigMap: &v1.ConfigMapVolumeSource{
				LocalObjectReference: v1.LocalObjectReference{
					Name: caPool.ConfigMap.Name,
				},
			},
		}
	} else if caPool.Secret != nil {
		caPoolSource = &v1.VolumeSource{
			Secret: &v1.SecretVolumeSource{
				SecretName: caPool.Secret.Name,
			},
		}
	}

	var extraVolumes []v1.Volume
	var extraVolumeMounts []v1.VolumeMount

	if caPoolSource != nil {
		extraVolumes = append(extraVolumes, v1.Volume{
			Name:         "ca-pool",
			VolumeSource: *caPoolSource,
		})
		extraVolumeMounts = append(extraVolumeMounts, v1.VolumeMount{
			Name:      "ca-pool",
			MountPath: caPool.MountPath,
			SubPath:   caPool.Key,
		})
	}

	token, err := tunnelClient.FetchTunnelToken(ctx)
	if err != nil {
		return errors.Wrap(err, "fetch tunnel token")
	}

	replicas, err := strconv.ParseInt(os.Getenv("CLOUDFLARED_REPLICA_COUNT"), 10, 32)
	if err != nil {
		return errors.Wrap(err, "invalid replica count")
	}

	deployment := cloudflaredConnectDeploymentTemplating(token, namespace, int32(replicas), extraVolumes, extraVolumeMounts)
	err = kubeClient.Create(ctx, deployment)
	if err != nil {
		return errors.Wrap(err, "create controlled-cloudflared-connector deployment")
	}
	return nil
}

func GetCaPoolOptions(configMap, secret, key, mountPath *string) *CaPool {
	var caPool *CaPool = nil
	if configMap != nil || secret != nil {
		caPool = &CaPool{
			Key:       "ca-certificates.crt",
			MountPath: "/etc/ssl/certs",
		}

		if configMap != nil {
			caPool.ConfigMap = &struct{ Name string }{Name: *configMap}
		}
		if secret != nil {
			caPool.Secret = &struct{ Name string }{Name: *secret}
		}
		if key != nil {
			caPool.Key = *key
		}
		if mountPath != nil {
			caPool.MountPath = *mountPath
		}
	}

	return caPool
}

func cloudflaredConnectDeploymentTemplating(
	token string,
	namespace string,
  replicas int32,
	extraVolumes []v1.Volume,
	extraVolumeMounts []v1.VolumeMount,
) *appsv1.Deployment {
	appName := "controlled-cloudflared-connector"
	image := os.Getenv("CLOUDFLARED_IMAGE")
	pullPolicy := os.Getenv("CLOUDFLARED_IMAGE_PULL_POLICY")

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appName,
			Namespace: namespace,
			Labels: map[string]string{
				"app": appName,
				"strrl.dev/cloudflare-tunnel-ingress-controller": "controlled-cloudflared-connector",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": appName,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: appName,
					Labels: map[string]string{
						"app": appName,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:            appName,
							Image:           image,
							ImagePullPolicy: v1.PullPolicy(pullPolicy),
							Command: []string{
								"cloudflared",
								"--no-autoupdate",
								"tunnel",
								"--metrics",
								"0.0.0.0:44483",
								"run",
								"--token",
								token,
							},
							VolumeMounts: extraVolumeMounts,
						},
					},
					RestartPolicy: v1.RestartPolicyAlways,
					Volumes:       extraVolumes,
				},
			},
		},
	}
}
