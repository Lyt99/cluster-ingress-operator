package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	oputil "github.com/openshift/cluster-ingress-operator/pkg/util"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// awsLBAdditionalResourceTags is a comma separated list of
	// Key=Value pairs that are additionally recorded on
	// load balancer resources and security groups.
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#aws-load-balancer-additional-resource-tags
	awsLBAdditionalResourceTags = "service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags"

	// awsLBProxyProtocolAnnotation is used to enable the PROXY protocol on any
	// AWS load balancer services created.
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#proxy-protocol-support-on-aws
	awsLBProxyProtocolAnnotation = "service.beta.kubernetes.io/aws-load-balancer-proxy-protocol"

	// AWSLBTypeAnnotation is a Service annotation used to specify an AWS load
	// balancer type. See the following for additional details:
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#aws-nlb-support
	AWSLBTypeAnnotation = "service.beta.kubernetes.io/aws-load-balancer-type"

	// AWSNLBAnnotation is the annotation value of an AWS Network Load Balancer (NLB).
	AWSNLBAnnotation = "nlb"

	// awsInternalLBAnnotation is the annotation used on a service to specify an AWS
	// load balancer as being internal.
	awsInternalLBAnnotation = "service.beta.kubernetes.io/aws-load-balancer-internal"

	// awsLBHealthCheckIntervalAnnotation is the approximate interval, in seconds, between AWS
	// load balancer health checks of an individual AWS instance. Defaults to 5, must be between
	// 5 and 300.
	awsLBHealthCheckIntervalAnnotation = "service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval"
	awsLBHealthCheckIntervalDefault    = "5"
	// Network Load Balancers require a health check interval of 10 or 30.
	// See https://docs.aws.amazon.com/elasticloadbalancing/latest/network/target-group-health-checks.html
	awsLBHealthCheckIntervalNLB = "10"

	// awsLBHealthCheckTimeoutAnnotation is the amount of time, in seconds, during which no response
	// means a failed AWS load balancer health check. The value must be less than the value of
	// awsLBHealthCheckIntervalAnnotation. Defaults to 4, must be between 2 and 60.
	awsLBHealthCheckTimeoutAnnotation = "service.beta.kubernetes.io/aws-load-balancer-healthcheck-timeout"
	awsLBHealthCheckTimeoutDefault    = "4"

	// awsLBHealthCheckUnhealthyThresholdAnnotation is the number of unsuccessful health checks required
	// for an AWS load balancer backend to be considered unhealthy for traffic. Defaults to 2, must be
	// between 2 and 10.
	awsLBHealthCheckUnhealthyThresholdAnnotation = "service.beta.kubernetes.io/aws-load-balancer-healthcheck-unhealthy-threshold"
	awsLBHealthCheckUnhealthyThresholdDefault    = "2"

	// awsLBHealthCheckHealthyThresholdAnnotation is the number of successive successful health checks
	// required for an AWS load balancer backend to be considered healthy for traffic. Defaults to 2,
	// must be between 2 and 10.
	awsLBHealthCheckHealthyThresholdAnnotation = "service.beta.kubernetes.io/aws-load-balancer-healthcheck-healthy-threshold"
	awsLBHealthCheckHealthyThresholdDefault    = "2"

	// iksLBScopeAnnotation is the annotation used on a service to specify an IBM
	// load balancer IP type.
	iksLBScopeAnnotation = "service.kubernetes.io/ibm-load-balancer-cloud-provider-ip-type"
	// iksLBScopePublic is the service annotation value used to specify an IBM load balancer
	// IP as public.
	iksLBScopePublic = "public"
	// iksLBScopePublic is the service annotation value used to specify an IBM load balancer
	// IP as private.
	iksLBScopePrivate = "private"

	// azureInternalLBAnnotation is the annotation used on a service to specify an Azure
	// load balancer as being internal.
	azureInternalLBAnnotation = "service.beta.kubernetes.io/azure-load-balancer-internal"

	// gcpLBTypeAnnotation is the annotation used on a service to specify a type of GCP
	// load balancer.
	gcpLBTypeAnnotation = "cloud.google.com/load-balancer-type"

	// GCPGlobalAccessAnnotation is the annotation used on an internal load balancer service
	// to enable the GCP Global Access feature.
	GCPGlobalAccessAnnotation = "networking.gke.io/internal-load-balancer-allow-global-access"

	// openstackInternalLBAnnotation is the annotation used on a service to specify an
	// OpenStack load balancer as being internal.
	openstackInternalLBAnnotation = "service.beta.kubernetes.io/openstack-internal-load-balancer"

	// localWithFallbackAnnotation is the annotation used on a service that
	// has "Local" external traffic policy to indicate that the service
	// proxy should prefer using a local endpoint but forward traffic to any
	// available endpoint if no local endpoint is available.
	localWithFallbackAnnotation = "traffic-policy.network.alpha.openshift.io/local-with-fallback"

	// alibabaCloudLBAddressTypeAnnotation is the annotation used on a service
	// to specify the network type of an Aliyun SLB
	alibabaCloudLBAddressTypeAnnotation = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-address-type"

	// alibabaCloudLBAddressTypeInternet is the service annotation value used to specify an Aliyun SLB
	// IP is exposed to the internet (public)
	alibabaCloudLBAddressTypeInternet = "internet"

	// alibabaCloudLBAddressTypeIntranet is the service annotation value used to specify an Aliyun SLB
	// IP is exposed to the intranet (private)
	alibabaCloudLBAddressTypeIntranet = "intranet"
)

var (
	// internalLBAnnotations maps platform to the annotation name and value
	// that tell the cloud provider that is associated with the platform
	// that the load balancer is internal.
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#internal-load-balancer
	InternalLBAnnotations = map[configv1.PlatformType]map[string]string{
		// Prior to 4.8, the aws internal LB annotation was set to "0.0.0.0/0".
		// While "0.0.0.0/0" is valid, the preferred value, according to the
		// documentation[1], is "true".
		// [1] https://kubernetes.io/docs/concepts/services-networking/service/#internal-load-balancer
		configv1.AWSPlatformType: {
			awsInternalLBAnnotation: "true",
		},
		configv1.AzurePlatformType: {
			// Azure load balancers are not customizable and are set to (2 fail @ 5s interval, 2 healthy)
			azureInternalLBAnnotation: "true",
		},
		// There are no required annotations for this platform as of
		// 2019-06-17 (but maybe MetalLB will have something for
		// internal load-balancers in the future).
		configv1.BareMetalPlatformType: nil,
		configv1.GCPPlatformType: {
			gcpLBTypeAnnotation: "Internal",
		},
		// There are no required annotations for this platform.
		configv1.LibvirtPlatformType: nil,
		configv1.OpenStackPlatformType: {
			openstackInternalLBAnnotation: "true",
		},
		configv1.NonePlatformType: nil,
		// vSphere does not support load balancers as of 2019-06-17.
		configv1.VSpherePlatformType: nil,
		configv1.IBMCloudPlatformType: {
			iksLBScopeAnnotation: iksLBScopePrivate,
		},
	}
)

// ensureLoadBalancerService creates an LB service if one is desired but absent.
// Always returns the current LB service if one exists (whether it already
// existed or was created during the course of the function).
func (r *reconciler) ensureLoadBalancerService(ci *operatorv1.IngressController, deploymentRef metav1.OwnerReference, infraConfig *configv1.Infrastructure) (bool, *corev1.Service, error) {
	platform, err := oputil.GetPlatformStatus(r.client, infraConfig)
	if err != nil {
		return false, nil, fmt.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ci.Namespace, ci.Name, err)
	}
	proxyNeeded, err := IsProxyProtocolNeeded(ci, platform)
	if err != nil {
		return false, nil, fmt.Errorf("failed to determine if proxy protocol is proxyNeeded for ingresscontroller %q: %v", ci.Name, err)
	}
	wantLBS, desiredLBService, err := desiredLoadBalancerService(ci, deploymentRef, platform, proxyNeeded)
	if err != nil {
		return false, nil, err
	}

	haveLBS, currentLBService, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !wantLBS && !haveLBS:
		return false, nil, nil
	case !wantLBS && haveLBS:
		if deletedFinalizer, err := r.deleteLoadBalancerServiceFinalizer(currentLBService); err != nil {
			return true, currentLBService, fmt.Errorf("failed to remove finalizer from load balancer service: %v", err)
		} else if deletedFinalizer {
			haveLBS, currentLBService, err = r.currentLoadBalancerService(ci)
			if err != nil {
				return haveLBS, currentLBService, err
			}
		}
		if err := r.deleteLoadBalancerService(currentLBService, &crclient.DeleteOptions{}); err != nil {
			return true, currentLBService, err
		}
		return false, nil, nil
	case wantLBS && !haveLBS:
		if err := r.createLoadBalancerService(desiredLBService); err != nil {
			return false, nil, err
		}
		return r.currentLoadBalancerService(ci)
	case wantLBS && haveLBS:
		if deletedFinalizer, err := r.deleteLoadBalancerServiceFinalizer(currentLBService); err != nil {
			return true, currentLBService, fmt.Errorf("failed to remove finalizer from load balancer service: %v", err)
		} else if deletedFinalizer {
			haveLBS, currentLBService, err = r.currentLoadBalancerService(ci)
			if err != nil {
				return haveLBS, currentLBService, err
			}
		}
		if updated, err := r.updateLoadBalancerService(currentLBService, desiredLBService, platform); err != nil {
			return true, currentLBService, fmt.Errorf("failed to update load balancer service: %v", err)
		} else if updated {
			return r.currentLoadBalancerService(ci)
		}
	}
	return true, currentLBService, nil
}

// desiredLoadBalancerService returns the desired LB service for a
// ingresscontroller, or nil if an LB service isn't desired. An LB service is
// desired if the high availability type is Cloud. An LB service will declare an
// owner reference to the given deployment.
func desiredLoadBalancerService(ci *operatorv1.IngressController, deploymentRef metav1.OwnerReference, platform *configv1.PlatformStatus, proxyNeeded bool) (bool, *corev1.Service, error) {
	if ci.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return false, nil, nil
	}
	service := manifests.LoadBalancerService()

	name := controller.LoadBalancerServiceName(ci)

	service.Namespace = name.Namespace
	service.Name = name.Name

	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	service.Labels["router"] = name.Name
	service.Labels[manifests.OwningIngressControllerLabel] = ci.Name

	service.Spec.Selector = controller.IngressControllerDeploymentPodSelector(ci).MatchLabels

	isInternal := ci.Status.EndpointPublishingStrategy.LoadBalancer != nil && ci.Status.EndpointPublishingStrategy.LoadBalancer.Scope == operatorv1.InternalLoadBalancer

	if service.Annotations == nil {
		service.Annotations = map[string]string{}
	}

	if proxyNeeded {
		service.Annotations[awsLBProxyProtocolAnnotation] = "*"
	}

	if platform != nil {
		if isInternal {
			annotation := InternalLBAnnotations[platform.Type]
			for name, value := range annotation {
				service.Annotations[name] = value
			}

			// Set the GCP Global Access annotation for internal load balancers on GCP only
			if platform.Type == configv1.GCPPlatformType {
				if ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters != nil &&
					ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.Type == operatorv1.GCPLoadBalancerProvider &&
					ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.GCP != nil {
					globalAccessEnabled := ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.GCP.ClientAccess == operatorv1.GCPGlobalAccess
					service.Annotations[GCPGlobalAccessAnnotation] = strconv.FormatBool(globalAccessEnabled)
				}
			}
		}
		switch platform.Type {
		case configv1.AWSPlatformType:
			if ci.Status.EndpointPublishingStrategy.LoadBalancer != nil &&
				ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters != nil &&
				ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.Type == operatorv1.AWSLoadBalancerProvider &&
				ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS != nil &&
				ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.Type == operatorv1.AWSNetworkLoadBalancer {
				service.Annotations[AWSLBTypeAnnotation] = AWSNLBAnnotation
				// NLBs require a different health check interval than CLBs
				service.Annotations[awsLBHealthCheckIntervalAnnotation] = awsLBHealthCheckIntervalNLB
			} else {
				service.Annotations[awsLBHealthCheckIntervalAnnotation] = awsLBHealthCheckIntervalDefault
			}

			if platform.AWS != nil && len(platform.AWS.ResourceTags) > 0 {
				var additionalTags []string
				for _, userTag := range platform.AWS.ResourceTags {
					if len(userTag.Key) > 0 {
						additionalTags = append(additionalTags, userTag.Key+"="+userTag.Value)
					}
				}
				if len(additionalTags) > 0 {
					service.Annotations[awsLBAdditionalResourceTags] = strings.Join(additionalTags, ",")
				}
			}

			// Set the load balancer for AWS to be as aggressive as Azure (2 fail @ 5s interval, 2 healthy)
			service.Annotations[awsLBHealthCheckTimeoutAnnotation] = awsLBHealthCheckTimeoutDefault
			service.Annotations[awsLBHealthCheckUnhealthyThresholdAnnotation] = awsLBHealthCheckUnhealthyThresholdDefault
			service.Annotations[awsLBHealthCheckHealthyThresholdAnnotation] = awsLBHealthCheckHealthyThresholdDefault
		case configv1.IBMCloudPlatformType:
			if !isInternal {
				service.Annotations[iksLBScopeAnnotation] = iksLBScopePublic
			}
			// Set ExternalTrafficPolicy to type Cluster - IBM's LoadBalancer impl is created within the cluster.
			// LB places VIP on one of the worker nodes, using keepalived to maintain the VIP and ensuring redundancy
			// LB relies on iptable rules kube-proxy puts in to send traffic from the VIP node to the cluster
			// If policy is local, traffic is only sent to pods on the local node, as such Cluster enables traffic to flow to  all the pods in the cluster
			service.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyTypeCluster
		case configv1.AlibabaCloudPlatformType:
			if isInternal {
				service.Annotations[alibabaCloudLBAddressTypeAnnotation] = alibabaCloudLBAddressTypeIntranet
			} else {
				service.Annotations[alibabaCloudLBAddressTypeAnnotation] = alibabaCloudLBAddressTypeInternet
			}
		}
		// Azure load balancers are not customizable and are set to (2 fail @ 5s interval, 2 healthy)
		// GCP load balancers are not customizable and are set to (3 fail @ 8s interval, 1 healthy)

		if v, err := shouldUseLocalWithFallback(ci, service); err != nil {
			return true, service, err
		} else if v {
			service.Annotations[localWithFallbackAnnotation] = ""
		}
	}

	service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	return true, service, nil
}

// shouldUseLocalWithFallback returns a Boolean value indicating whether the
// local-with-fallback annotation should be set for the given service, and
// returns an error if the given ingresscontroller has an invalid unsupported
// config override.
func shouldUseLocalWithFallback(ic *operatorv1.IngressController, service *corev1.Service) (bool, error) {
	// By default, use local-with-fallback when using the "Local" external
	// traffic policy.
	if service.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyTypeLocal {
		return false, nil
	}

	// Allow the user to override local-with-fallback.
	if len(ic.Spec.UnsupportedConfigOverrides.Raw) > 0 {
		var unsupportedConfigOverrides struct {
			LocalWithFallback string `json:"localWithFallback"`
		}
		if err := json.Unmarshal(ic.Spec.UnsupportedConfigOverrides.Raw, &unsupportedConfigOverrides); err != nil {
			return false, fmt.Errorf("ingresscontroller %q has invalid spec.unsupportedConfigOverrides: %w", ic.Name, err)
		}
		override := unsupportedConfigOverrides.LocalWithFallback
		if len(override) != 0 {
			if val, err := strconv.ParseBool(override); err != nil {
				return false, fmt.Errorf("ingresscontroller %q has invalid spec.unsupportedConfigOverrides.localWithFallback: %w", ic.Name, err)
			} else {
				return val, nil
			}
		}
	}

	return true, nil
}

// currentLoadBalancerService returns any existing LB service for the
// ingresscontroller.
func (r *reconciler) currentLoadBalancerService(ci *operatorv1.IngressController) (bool, *corev1.Service, error) {
	service := &corev1.Service{}
	if err := r.client.Get(context.TODO(), controller.LoadBalancerServiceName(ci), service); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, service, nil
}

// deleteLoadBalancerServiceFinalizer removes the
// "ingress.openshift.io/operator" finalizer from the provided load balancer
// service.  This finalizer used to be needed for helping with DNS cleanup, but
// that's no longer necessary.  We just need to clear the finalizer which might
// exist on existing resources.
// TODO: Delete this method and all calls to it after 4.8.
func (r *reconciler) deleteLoadBalancerServiceFinalizer(service *corev1.Service) (bool, error) {
	if !slice.ContainsString(service.Finalizers, manifests.LoadBalancerServiceFinalizer) {
		return false, nil
	}

	// Mutate a copy to avoid assuming we know where the current one came from
	// (i.e. it could have been from a cache).
	updated := service.DeepCopy()
	updated.Finalizers = slice.RemoveString(updated.Finalizers, manifests.LoadBalancerServiceFinalizer)
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, fmt.Errorf("failed to remove finalizer from service %s/%s: %v", service.Namespace, service.Name, err)
	}

	log.Info("removed finalizer from load balancer service", "namespace", service.Namespace, "name", service.Name)

	return true, nil
}

// finalizeLoadBalancerService removes the "ingress.openshift.io/operator" finalizer
// from the load balancer service of ci. This was for helping with DNS cleanup, but
// that's no longer necessary. We just need to clear the finalizer which might exist
// on existing resources.
func (r *reconciler) finalizeLoadBalancerService(ci *operatorv1.IngressController) (bool, error) {
	haveLBS, service, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return false, err
	}
	if !haveLBS {
		return false, nil
	}
	_, err = r.deleteLoadBalancerServiceFinalizer(service)
	return true, err
}

// createLoadBalancerService creates a load balancer service.
func (r *reconciler) createLoadBalancerService(service *corev1.Service) error {
	if err := r.client.Create(context.TODO(), service); err != nil {
		return fmt.Errorf("failed to create load balancer service %s/%s: %v", service.Namespace, service.Name, err)
	}
	log.Info("created load balancer service", "namespace", service.Namespace, "name", service.Name)
	return nil
}

// deleteLoadBalancerService deletes a load balancer service.
func (r *reconciler) deleteLoadBalancerService(service *corev1.Service, options *crclient.DeleteOptions) error {
	if err := r.client.Delete(context.TODO(), service, options); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete load balancer service %s/%s: %v", service.Namespace, service.Name, err)
	}
	log.Info("deleted load balancer service", "namespace", service.Namespace, "name", service.Name)
	return nil
}

// updateLoadBalancerService updates a load balancer service.  Returns a Boolean
// indicating whether the service was updated, and an error value.
func (r *reconciler) updateLoadBalancerService(current, desired *corev1.Service, platform *configv1.PlatformStatus) (bool, error) {
	changed, updated := loadBalancerServiceChanged(current, desired)
	if !changed {
		return false, nil
	}
	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	log.Info("updated load balancer service", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// managedLoadBalancerServiceAnnotations is a set of annotation keys for
// annotations that the operator manages for LoadBalancer-type services.
var managedLoadBalancerServiceAnnotations = sets.NewString(
	awsLBHealthCheckIntervalAnnotation,
	GCPGlobalAccessAnnotation,
	localWithFallbackAnnotation,
)

// loadBalancerServiceChanged checks if the current load balancer service
// matches the expected and if not returns an updated one.
func loadBalancerServiceChanged(current, expected *corev1.Service) (bool, *corev1.Service) {
	annotationCmpOpts := []cmp.Option{
		cmpopts.IgnoreMapEntries(func(k, _ string) bool {
			return !managedLoadBalancerServiceAnnotations.Has(k)
		}),
	}
	if cmp.Equal(current.Annotations, expected.Annotations, annotationCmpOpts...) {
		return false, nil
	}

	updated := current.DeepCopy()

	// Preserve everything but the AWS LB health check interval annotation,
	// GCP Global Access internal Load Balancer annotation, and
	// local-with-fallback annotation
	// (see <https://bugzilla.redhat.com/show_bug.cgi?id=1908758>).
	// Updating annotations and spec fields cannot be done unless the
	// previous release blocks upgrades when the user has modified those
	// fields (see <https://bugzilla.redhat.com/show_bug.cgi?id=1905490>).
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}

	for annotation := range managedLoadBalancerServiceAnnotations {
		currentVal, have := current.Annotations[annotation]
		expectedVal, want := expected.Annotations[annotation]
		if want && (!have || currentVal != expectedVal) {
			updated.Annotations[annotation] = expected.Annotations[annotation]
		} else if have && !want {
			delete(updated.Annotations, annotation)
		}
	}

	return true, updated
}
