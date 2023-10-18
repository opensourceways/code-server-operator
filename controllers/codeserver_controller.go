/*
Copyright 2019 tommylikehu@gmail.com.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	errrorlib "errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/opensourceways/code-server-operator/controllers/initplugins"
	"github.com/opensourceways/code-server-operator/controllers/initplugins/interface"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ingressv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	resourcev1 "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	csv1alpha1 "github.com/opensourceways/code-server-operator/api/v1alpha1"
)

const (
	CSNAME           = "code-server"
	MaxActiveSeconds = 60 * 60 * 24
	MaxKeepSeconds   = 60 * 60 * 24 * 30
	HttpPort         = 8080
	DefaultWorkspace = "/workspace"
	IngressLimitKey  = "kubernetes.io/ingress-bandwidth"
	EgressLimitKey   = "kubernetes.io/egress-bandwidth"
	StorageEmptyDir  = "emptyDir"
	InstanceEndpoint = "instanceEndpoint"
	TerminalIngress  = "%s-terminal"
)

// CodeServerReconciler reconciles a CodeServer object
type CodeServerReconciler struct {
	client.Client
	Log     logr.Logger
	Scheme  *runtime.Scheme
	Options *CodeServerOption
	ReqCh   chan CodeServerRequest
}

// +kubebuilder:rbac:groups=cs.opensourceways.com,resources=codeservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cs.opensourceways.com,resources=codeservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=,resources=endpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=,resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=extensions,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=extensions,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=,resources=secrets,verbs=get;list;watch
func (r *CodeServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reQueueInterval := -1
	_ = context.Background()
	reqLogger := r.Log.WithValues("codeserver", req.NamespacedName)
	// Fetch the CodeServer instance
	codeServer := &csv1alpha1.CodeServer{}
	err := r.Client.Get(context.TODO(), req.NamespacedName, codeServer)
	if err != nil {
		if errors.IsNotFound(err) {
			reqLogger.Info("CodeServer has been deleted. Trying to delete its related resources.")
			r.deleteFromInactiveWatch(req.NamespacedName)
			r.deleteFromRecycleWatch(req.NamespacedName)
			if err := r.deleteCodeServerResource(req.Name, req.Namespace, codeServer.Spec.StorageName,
				true); err != nil {
				return reconcile.Result{Requeue: true}, err
			}
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		reqLogger.Error(err, "Failed to get CoderServer.")
		return reconcile.Result{}, err
	}

	//case1. code server now stays inactive, we will delete all resources except volume.
	//case2. code server will be directly deleted after RecycleAfterSeconds if.
	//       InactiveAfterSeconds are configured with zero.
	//case3. code server now stays recycled, we will delete all resources.
	//case4. reconcile the code server.
	if HasCondition(codeServer.Status, csv1alpha1.ServerInactive) && !HasCondition(codeServer.Status, csv1alpha1.ServerRecycled) {
		//remove it from watch list and add it to recycle watch
		r.deleteFromInactiveWatch(req.NamespacedName)
		inActiveCondition := GetCondition(codeServer.Status, csv1alpha1.ServerInactive)
		if (codeServer.Spec.RecycleAfterSeconds == nil) || *codeServer.Spec.RecycleAfterSeconds <= 0 || *codeServer.Spec.RecycleAfterSeconds >= MaxKeepSeconds {
			// we keep the instance within MaxKeepSeconds maximumly
			reqLogger.Info(fmt.Sprintf("Code server will be recycled after %d seconds.",
				MaxKeepSeconds))
			r.addToRecycleWatch(req.NamespacedName, MaxKeepSeconds, inActiveCondition.LastTransitionTime, false)
		} else {
			reqLogger.Info(fmt.Sprintf("Code server will be recycled after %d seconds.",
				*codeServer.Spec.RecycleAfterSeconds))
			r.addToRecycleWatch(req.NamespacedName, *codeServer.Spec.RecycleAfterSeconds, inActiveCondition.LastTransitionTime, false)
		}
		if err := r.deleteCodeServerResource(codeServer.Name, codeServer.Namespace, codeServer.Spec.StorageName,
			false); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
	} else if !HasCondition(codeServer.Status, csv1alpha1.ServerRecycled) &&
		*codeServer.Spec.InactiveAfterSeconds == 0 && HasCondition(codeServer.Status, csv1alpha1.ServerReady) {
		current := metav1.Time{
			Time: time.Now(),
		}
		boundStatus := GetCondition(codeServer.Status, csv1alpha1.ServerBound)
		if (codeServer.Spec.RecycleAfterSeconds == nil) || *codeServer.Spec.RecycleAfterSeconds <= 0 || *codeServer.Spec.RecycleAfterSeconds >= MaxKeepSeconds {
			// status nil for old code server which doesn't have a bound condition
			if boundStatus != nil && boundStatus.Status == corev1.ConditionTrue {
				// we keep the instance within MaxKeepSeconds maximumly
				reqLogger.Info(fmt.Sprintf("Code server will be recycled after %d seconds.",
					MaxKeepSeconds))
				r.addToRecycleWatch(req.NamespacedName, MaxKeepSeconds, current, codeServer.Spec.IncreaseRecycleSeconds)
			}
		} else {
			if boundStatus != nil && boundStatus.Status == corev1.ConditionTrue {
				reqLogger.Info(fmt.Sprintf("Code server will be recycled after %d seconds.",
					*codeServer.Spec.RecycleAfterSeconds))
				r.addToRecycleWatch(req.NamespacedName, *codeServer.Spec.RecycleAfterSeconds, current, codeServer.Spec.IncreaseRecycleSeconds)
			}
		}
	} else if HasCondition(codeServer.Status, csv1alpha1.ServerRecycled) {
		//remove it from watch list
		r.deleteFromInactiveWatch(req.NamespacedName)
		r.deleteFromRecycleWatch(req.NamespacedName)
		if err := r.deleteCodeServerResource(codeServer.Name, codeServer.Namespace, codeServer.Spec.StorageName,
			true); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
	} else {
		var failed error
		var service *corev1.Service
		var deployment *appsv1.Deployment
		var condition csv1alpha1.ServerCondition
		// 0/5 check whether tls secret exists
		_, failed = r.findLegalCertSecrets(codeServer.Name, codeServer.Namespace, r.Options.HttpsSecretName)
		if failed == nil {
			// check LxdClientSecretName secret if needed
			if strings.EqualFold(string(codeServer.Spec.Runtime), string(csv1alpha1.RuntimeLxd)) {
				if len(r.Options.LxdClientSecretName) == 0 {
					_, failed = r.findLegalCertSecrets(codeServer.Name, codeServer.Namespace, r.Options.LxdClientSecretName)
				}
			}
		}
		// 1/5: reconcile PVC
		if failed == nil {
			if r.needDeployPVC(codeServer.Spec.StorageName) {
				_, failed = r.reconcileForPVC(codeServer)
			}
		}
		// 2/5: reconcile service
		if failed == nil {
			service, failed = r.reconcileForService(codeServer)
		}
		// 3/5:reconcile ingress
		if failed == nil {
			_, failed = r.reconcileForIngress(codeServer)
		}
		// 4/5: reconcile deployment
		if failed == nil {
			deployment, failed = r.reconcileForDeployment(codeServer)
		}
		// 5/5: update code server status
		createCondition := false
		if !HasCondition(codeServer.Status, csv1alpha1.ServerCreated) {
			createdCondition := NewStateCondition(csv1alpha1.ServerCreated,
				"code server has been accepted", map[string]string{}, corev1.ConditionTrue)
			createCondition = SetCondition(&codeServer.Status, createdCondition)
		}
		if failed == nil {
			condition = NewStateCondition(csv1alpha1.ServerReady,
				"code server now available", map[string]string{}, corev1.ConditionTrue)
			if !HasDeploymentCondition(deployment.Status, appsv1.DeploymentAvailable) || !r.serverReady(
				codeServer) {
				condition.Status = corev1.ConditionFalse
				condition.Reason = "waiting deployment to be available and endpoint ready"
				//only when deployment is ready while server unready, try to watch it later
				if HasDeploymentCondition(deployment.Status, appsv1.DeploymentAvailable) {
					reqLogger.Info("Code server will be requeue due to endpoint unready")
					reQueueInterval = 5
				}
			} else {
				//add it to watch list
				var endPoint string
				// No matter tls is enabled or nor we both expose upstream via http for internal probe
				endPoint = fmt.Sprintf("http://%s:%d/%s", service.Spec.ClusterIP, HttpPort,
					strings.TrimLeft(codeServer.Spec.ConnectProbe, "/"))
				condition.Message[InstanceEndpoint] = r.getInstanceEndpoint(codeServer)

				boundStatus := GetCondition(codeServer.Status, csv1alpha1.ServerBound)
				if (codeServer.Spec.InactiveAfterSeconds == nil) || *codeServer.Spec.InactiveAfterSeconds < 0 || *codeServer.Spec.InactiveAfterSeconds >= MaxActiveSeconds {
					// we keep the instance within MaxActiveSeconds maximumly
					if boundStatus != nil && boundStatus.Status == corev1.ConditionTrue {
						r.addToInactiveWatch(req.NamespacedName, MaxActiveSeconds, endPoint)
						reqLogger.Info(fmt.Sprintf("Code server will be disactived after %d non-connection.",
							MaxActiveSeconds))
					}
				} else if *codeServer.Spec.InactiveAfterSeconds == 0 {
					// we will not watch this code server instance if inactive is set 0
					reqLogger.Info("Code server will never be disactived")
				} else {
					if boundStatus != nil && boundStatus.Status == corev1.ConditionTrue {
						r.addToInactiveWatch(req.NamespacedName, *codeServer.Spec.InactiveAfterSeconds, endPoint)
						reqLogger.Info(fmt.Sprintf("Code server will be disactived after %d non-connection.",
							*codeServer.Spec.InactiveAfterSeconds))
					}
				}

			}
		} else {
			condition = NewStateCondition(csv1alpha1.ServerErrored,
				"code server errored", map[string]string{"detail": failed.Error()}, corev1.ConditionTrue)
		}
		updateCondition := SetCondition(&codeServer.Status, condition)
		boundCondition := false
		//if it's ready and missing server bound status, add default condition here.
		if HasCondition(codeServer.Status, csv1alpha1.ServerReady) && MissingCondition(
			codeServer.Status, csv1alpha1.ServerBound) {
			additionCondition := NewStateCondition(csv1alpha1.ServerBound,
				"code server waiting to be bound", map[string]string{}, corev1.ConditionFalse)
			boundCondition = SetCondition(&codeServer.Status, additionCondition)
		}
		if createCondition || updateCondition || boundCondition {
			updateStatus := codeServer.Status
			err = r.Client.Get(context.TODO(), req.NamespacedName, codeServer)
			if err != nil {
				reqLogger.Error(err, "Failed to get CoderServer object for update.")
				return reconcile.Result{Requeue: true}, err
			}
			codeServer.Status = updateStatus
			err = r.Client.Update(context.TODO(), codeServer)
			if err != nil {
				reqLogger.Error(err, "Failed to update code server status.")
				return reconcile.Result{Requeue: true}, nil
			}
		}
		if failed != nil {
			return reconcile.Result{
				Requeue:      true,
				RequeueAfter: time.Second * 20}, failed
		}
	}
	if reQueueInterval >= 0 {
		return reconcile.Result{Requeue: true, RequeueAfter: time.Second * time.Duration(reQueueInterval)}, nil
	}
	return reconcile.Result{Requeue: false}, nil

}

func (r *CodeServerReconciler) addToInactiveWatch(resource types.NamespacedName, duration int64, endpoint string) {
	request := CodeServerRequest{
		resource: resource,
		duration: duration,
		operate:  AddInactiveWatch,
		endpoint: endpoint,
	}
	r.ReqCh <- request
}

func (r *CodeServerReconciler) deleteFromInactiveWatch(resource types.NamespacedName) {
	request := CodeServerRequest{
		resource: resource,
		operate:  DeleteInactiveWatch,
	}
	r.ReqCh <- request
}

func (r *CodeServerReconciler) findLegalCertSecrets(name, namespace, secretName string) (*corev1.Secret, error) {
	reqLogger := r.Log.WithValues("namespace", namespace, "name", name)
	tlsSecret := &corev1.Secret{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: namespace}, tlsSecret)
	if err == nil {
		if _, ok := tlsSecret.Data[corev1.TLSCertKey]; !ok {
			reqLogger.Info(
				fmt.Sprintf("could not found secret key %s in secret %s", corev1.TLSCertKey, secretName))
			return nil, errrorlib.New(fmt.Sprintf("could not found secret key %s in secret %s", corev1.TLSCertKey, secretName))
		}
		if _, ok := tlsSecret.Data[corev1.TLSPrivateKeyKey]; !ok {
			reqLogger.Info(
				fmt.Sprintf("could not found secret key %s in secret %s", corev1.TLSPrivateKeyKey, secretName))
			return nil, errrorlib.New(fmt.Sprintf("could not found secret key %s in secret %s", corev1.TLSPrivateKeyKey, secretName))
		}
		reqLogger.Info(fmt.Sprintf("found secret %s in cluster", secretName))
		return tlsSecret, nil
	}
	reqLogger.Info(fmt.Sprintf("could not found secret %s in cluster, reconciling related code server in this namespace will be disabled.", secretName))
	return nil, err
}

func (r *CodeServerReconciler) addToRecycleWatch(resource types.NamespacedName, duration int64, inactivetime metav1.Time, increaseRecycleSeconds bool) {
	request := CodeServerRequest{
		resource:               resource,
		operate:                AddRecycleWatch,
		duration:               duration,
		inactiveTime:           inactivetime,
		increaseRecycleSeconds: increaseRecycleSeconds,
	}
	r.ReqCh <- request
}

func (r *CodeServerReconciler) deleteFromRecycleWatch(resource types.NamespacedName) {
	request := CodeServerRequest{
		resource: resource,
		operate:  DeleteRecycleWatch,
	}
	r.ReqCh <- request
}

func (r *CodeServerReconciler) deleteCodeServerResource(name, namespace string, storageName string, includePVC bool) error {
	reqLogger := r.Log.WithValues("namespace", name, "name", namespace)
	reqLogger.Info("Deleting code server resources.")
	//delete ingress
	ing := &ingressv1.Ingress{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, ing)
	//error of getting object is ignored
	if err == nil {
		err = r.Client.Delete(context.TODO(), ing)
		if err != nil {
			return err
		}
		reqLogger.Info("ingress resource has been successfully deleted.")
	} else if !errors.IsNotFound(err) {
		reqLogger.Info(fmt.Sprintf("failed to get ingress resource for deletion: %v", err))
	}
	//delete service
	srv := &corev1.Service{}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, srv)
	//error of getting object is ignored
	if err == nil {
		err = r.Client.Delete(context.TODO(), srv)
		if err != nil {
			return err
		}
		reqLogger.Info("service resource has been successfully deleted.")
	} else if !errors.IsNotFound(err) {
		reqLogger.Info(fmt.Sprintf("failed to get service resource for deletion: %v", err))
	}
	//delete deployment
	app := &appsv1.Deployment{}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, app)
	//error of getting object is ignored
	if err == nil {
		err = r.Client.Delete(context.TODO(), app)
		if err != nil {
			return err
		}
		reqLogger.Info("development resource has been successfully deleted.")
	} else if !errors.IsNotFound(err) {
		reqLogger.Info(fmt.Sprintf("failed to get development resource for deletion: %v", err))
	}
	if includePVC && r.needDeployPVC(storageName) {
		//delete pvc
		pvc := &corev1.PersistentVolumeClaim{}
		err = r.Client.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, pvc)
		//error of getting object is ignored
		if err == nil {
			err = r.Client.Delete(context.TODO(), pvc)
			if err != nil {
				return err
			}
			reqLogger.Info("PVC resource has been successfully deleted.")
		} else if !errors.IsNotFound(err) {
			reqLogger.Info(fmt.Sprintf("failed to get PVC resource for deletion: %v", err))
		}
	}
	return nil
}

func (r *CodeServerReconciler) reconcileForPVC(codeServer *csv1alpha1.CodeServer) (*corev1.PersistentVolumeClaim, error) {
	reqLogger := r.Log.WithValues("namespace", codeServer.Namespace, "name", codeServer.Name)
	reqLogger.Info("Reconciling persistent volume claim.")
	//reconcile pvc for code server
	newPvc, err := r.newPVC(codeServer)
	if err != nil {
		reqLogger.Error(err, "Failed to create new PersistentVolumeClaim.")
		return nil, err
	}
	oldPvc := &corev1.PersistentVolumeClaim{}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: codeServer.Name, Namespace: codeServer.Namespace}, oldPvc)
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating a PersistentVolumeClaim.")
		err = r.Client.Create(context.TODO(), newPvc)
		if err != nil {
			reqLogger.Error(err, "Failed to create PersistentVolumeClaim.")
			return nil, err
		}
		return newPvc, nil
	} else {
		if err != nil {
			//Reschedule the event
			reqLogger.Error(err, fmt.Sprintf("Failed to get PVC for %s.", codeServer.Name))
			return nil, err
		}
		if needUpdatePVC(oldPvc, newPvc) {
			reqLogger.Error(err, "Updating PersistentVolumeClaim is not supported.")
			return oldPvc, nil
		}
	}
	return oldPvc, nil
}

func (r *CodeServerReconciler) serverReady(codeServer *csv1alpha1.CodeServer) bool {
	reqLogger := r.Log.WithValues("namespace", codeServer.Namespace, "name", codeServer.Name)
	reqLogger.Info("Waiting Service Ready.")
	instEndpoint := ""
	instEndpoint = fmt.Sprintf("https://%s.%s/%s", codeServer.Spec.Subdomain, r.Options.DomainName,
		strings.TrimLeft(codeServer.Spec.ConnectProbe, "/"))
	resp, err := http.Get(instEndpoint)
	if err != nil {
		reqLogger.Error(err, fmt.Sprintf("failed to detect instance endpoint for code server %s",
			codeServer.Name))
		return false
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
		return true
	}
	reqLogger.Info(fmt.Sprintf("instance endpoint still unready for code server %s, status code %d, endpoint is %s",
		codeServer.Name, resp.StatusCode, instEndpoint))
	return false
}
func (r *CodeServerReconciler) reconcileForDeployment(codeServer *csv1alpha1.CodeServer) (*appsv1.Deployment, error) {
	reqLogger := r.Log.WithValues("namespace", codeServer.Namespace, "name", codeServer.Name)
	reqLogger.Info("Reconciling Deployment.")
	//reconcile pvc for code server
	newDev, err := r.newDeployment(codeServer)
	if err != nil {
		reqLogger.Error(err, "Failed to generate Deployment.")
		return nil, err
	}
	oldDev := &appsv1.Deployment{}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: codeServer.Name, Namespace: codeServer.Namespace}, oldDev)
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating a Deployment.")
		err = r.Client.Create(context.TODO(), newDev)
		if err != nil {
			reqLogger.Error(err, "Failed to create Deployment.")
			return nil, err
		}
	} else {
		if err != nil {
			//Reschedule the event
			reqLogger.Error(err, fmt.Sprintf("Failed to get Deployment for %s.", codeServer.Name))
			return nil, err
		}
		if needUpdateDeployment(oldDev, newDev) {
			oldDev.Spec = newDev.Spec
			reqLogger.Info("Updating a Development.")
			err = r.Client.Update(context.TODO(), oldDev)
			if err != nil {
				reqLogger.Error(err, "Failed to update Deployment.")
				return nil, err
			}
		}
	}
	return oldDev, nil
}

func (r *CodeServerReconciler) reconcileForIngress(codeServer *csv1alpha1.CodeServer) (*ingressv1.Ingress, error) {
	reqLogger := r.Log.WithValues("namespace", codeServer.Namespace, "name", codeServer.Name)
	reqLogger.Info("Reconciling ingress.")
	//reconcile ingress for code server
	newIngress := r.NewIngress(codeServer)
	oldIngress := &ingressv1.Ingress{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: fmt.Sprintf(TerminalIngress, codeServer.Name), Namespace: codeServer.Namespace}, oldIngress)
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating an ingress.")
		err = r.Client.Create(context.TODO(), newIngress)
		if err != nil {
			reqLogger.Error(err, "Failed to create ingress.")
			return nil, err
		}
		// if update is required
	} else {
		if err != nil {
			//Reschedule the event
			reqLogger.Error(err, fmt.Sprintf("Failed to get Ingress for %s.", codeServer.Name))
			return nil, err
		}
		if !equality.Semantic.DeepEqual(oldIngress.Spec, newIngress.Spec) {
			oldIngress.Spec = newIngress.Spec
			reqLogger.Info("Updating an ingress.")
			err = r.Client.Update(context.TODO(), oldIngress)
			if err != nil {
				reqLogger.Error(err, "Failed to update ingress.")
				return nil, err
			}
		}
	}
	return oldIngress, nil
}

func (r *CodeServerReconciler) reconcileForService(codeServer *csv1alpha1.CodeServer) (*corev1.Service, error) {
	reqLogger := r.Log.WithValues("namespace", codeServer.Namespace, "name", codeServer.Name)
	reqLogger.Info("Reconciling service.")
	//reconcile service for code server
	newService := r.newService(codeServer)
	oldService := &corev1.Service{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: codeServer.Name, Namespace: codeServer.Namespace}, oldService)
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating a Service.")
		err = r.Client.Create(context.TODO(), newService)
		if err != nil {
			reqLogger.Error(err, "Failed to create Service.")
			return nil, err
		}
		// if update is required
	} else {
		if err != nil {
			//Reschedule the event
			reqLogger.Error(err, fmt.Sprintf("Failed to get Service for %s.", codeServer.Name))
			return nil, err
		}
		if needUpdateService(oldService, newService) {
			oldService.Spec = newService.Spec
			reqLogger.Info("Updating a Service.")
			err = r.Client.Update(context.TODO(), oldService)
			if err != nil {
				reqLogger.Error(err, "Failed to update Service.")
				return nil, err
			}
		}
	}
	return oldService, nil
}

func (r *CodeServerReconciler) addInitContainersForDeployment(m *csv1alpha1.CodeServer, baseDir, baseDirVolume string) []corev1.Container {
	var containers []corev1.Container
	reqLogger := r.Log.WithValues("namespace", m.Namespace, "name", m.Name)
	if len(m.Spec.InitContainers) != 0 {
		for _, container := range m.Spec.InitContainers {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				MountPath: baseDir,
				Name:      baseDirVolume,
			})
			containers = append(containers, container)
		}
	}
	if len(m.Spec.InitPlugins) == 0 {
		return containers
	}
	clientSet := _interface.PluginClients{Client: r.Client}
	for p, arguments := range m.Spec.InitPlugins {
		plugin, err := initplugins.CreatePlugin(clientSet, p, arguments, baseDir)
		if err != nil {
			reqLogger.Error(err, fmt.Sprintf("Failed to initialize init plugin %s", p))
			continue
		}
		container := plugin.GenerateInitContainerSpec()
		//Add work space volume in default
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			MountPath: baseDir,
			Name:      baseDirVolume,
		})
		containers = append(containers, *container)
	}
	return containers
}

func (r *CodeServerReconciler) newDeployment(m *csv1alpha1.CodeServer) (*appsv1.Deployment, error) {
	instanceRuntime := string(m.Spec.Runtime)
	if strings.EqualFold(instanceRuntime, string(csv1alpha1.RuntimeCode)) {
		//Create code server environment with vs code
		return r.deploymentForVSCodeServer(m), nil
	} else if strings.EqualFold(instanceRuntime, string(csv1alpha1.RuntimeGotty)) || strings.EqualFold(instanceRuntime, string(csv1alpha1.RuntimePGWeb)) || strings.EqualFold(instanceRuntime, string(csv1alpha1.RuntimeGeneric)) {
		//Create code server environment with generic container
		return r.deploymentForGeneric(m), nil
	} else if strings.EqualFold(instanceRuntime, string(csv1alpha1.RuntimeLxd)) {
		//Create code server environment with gotty based terminal which runs on lxd
		return r.deploymentForLxd(m), nil
	} else {
		return nil, errrorlib.New(fmt.Sprintf("unsupported runtime %s", m.Spec.Runtime))
	}
}

func (r *CodeServerReconciler) getInstanceEndpoint(m *csv1alpha1.CodeServer) string {
	instanceRuntime := string(m.Spec.Runtime)
	if strings.EqualFold(instanceRuntime, string(csv1alpha1.RuntimeGotty)) || strings.EqualFold(instanceRuntime, string(csv1alpha1.RuntimeLxd)) {
		return fmt.Sprintf("wss://%s.%s/ws", m.Spec.Subdomain, r.Options.DomainName)
	} else if strings.EqualFold(instanceRuntime, string(csv1alpha1.RuntimeGeneric)) && len(m.Spec.ConnectionString) != 0 {
		return fmt.Sprintf(m.Spec.ConnectionString, m.Spec.Subdomain, r.Options.DomainName)
	} else {
		return fmt.Sprintf("https://%s.%s/", m.Spec.Subdomain, r.Options.DomainName)
	}
}

// deploymentForVSCodeServer returns a code server with VSCode Deployment object
func (r *CodeServerReconciler) deploymentForVSCodeServer(m *csv1alpha1.CodeServer) *appsv1.Deployment {
	reqLogger := r.Log.WithValues("namespace", m.Namespace, "name", m.Name)
	baseCodeDir := "/home/coder/project"
	baseCodeVolume := "code-server-project-dir"
	ls := appLabel(m.Name, m.Labels)
	replicas := int32(1)
	enablePriviledge := m.Spec.Privileged
	priviledged := corev1.SecurityContext{
		Privileged: enablePriviledge,
	}

	//share volume used for status watch
	shareQuantity, _ := resourcev1.ParseQuantity("500M")
	shareVolume := corev1.EmptyDirVolumeSource{
		Medium:    "",
		SizeLimit: &shareQuantity,
	}
	var arguments []string
	arguments = append(arguments, []string{"--port", strconv.Itoa(HttpPort)}...)
	arguments = append(arguments, []string{"--verbose"}...)
	arguments = append(arguments, baseCodeDir)

	initContainer := r.addInitContainersForDeployment(m, baseCodeDir, baseCodeVolume)
	reqLogger.Info(fmt.Sprintf("init containers has been injected into deployment %v", initContainer))

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					NodeSelector:   m.Spec.NodeSelector,
					InitContainers: initContainer,
					Containers: []corev1.Container{
						{
							Image:           m.Spec.Image,
							Name:            CSNAME,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args:            arguments,
							SecurityContext: &priviledged,
							Env:             m.Spec.Envs,
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/home/coder/.local/share/code-server",
									Name:      "code-server-share-dir",
								},
								{
									MountPath: baseCodeDir,
									Name:      baseCodeVolume,
								},
							},
							Ports: []corev1.ContainerPort{{
								ContainerPort: HttpPort,
								Name:          "serverhttpport",
							}},
						},
						{
							Image:           r.Options.VSExporterImage,
							Name:            "status-exporter",
							ImagePullPolicy: corev1.PullIfNotPresent,
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/home/coder/.local/share/code-server",
									Name:      "code-server-share-dir",
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "STAT_FILE",
									Value: "/home/coder/.local/share/code-server/heartbeat",
								},
								{
									Name:  "LISTEN_PORT",
									Value: "8000",
								},
							},
							Ports: []corev1.ContainerPort{{
								ContainerPort: 8000,
								Name:          "statusreporter",
							}},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "code-server-share-dir",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &shareVolume,
							},
						},
					},
				},
			},
		},
	}

	// add volume pvc pr emptyDir
	if r.needDeployPVC(m.Spec.StorageName) {
		dataVolume := corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: m.Name,
		}
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: baseCodeVolume,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &dataVolume,
			},
		})
	} else {
		volumeQuantity, _ := resourcev1.ParseQuantity(m.Spec.StorageSize)
		dataVolume := corev1.EmptyDirVolumeSource{
			SizeLimit: &volumeQuantity,
		}
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: baseCodeVolume,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &dataVolume,
			},
		})
	}
	// Set CodeServer instance as the owner of the Deployment.
	controllerutil.SetControllerReference(m, dep, r.Scheme)
	return dep
}

// deploymentForGeneric returns a code server with generic temporary environments
func (r *CodeServerReconciler) deploymentForGeneric(m *csv1alpha1.CodeServer) *appsv1.Deployment {
	reqLogger := r.Log.WithValues("namespace", m.Namespace, "name", m.Name)
	ls := appLabel(m.Name, m.Labels)
	baseCodeDir := r.getDefaultWorkSpace(m)
	baseCodeVolume := "code-server-workspace"
	replicas := int32(1)
	enablePriviledge := m.Spec.Privileged
	priviledged := corev1.SecurityContext{
		Privileged: enablePriviledge,
	}
	initContainer := r.addInitContainersForDeployment(m, baseCodeDir, baseCodeVolume)
	reqLogger.Info(fmt.Sprintf("init containers has been injected into deployment %v", initContainer))
	//convert liveness or readiness probe
	if m.Spec.LivenessProbe != nil {
		if m.Spec.LivenessProbe.HTTPGet != nil {
			m.Spec.LivenessProbe.HTTPGet.Port = r.getContainerPort(m)
		}
	}
	if m.Spec.ReadinessProbe != nil {
		if m.Spec.ReadinessProbe.HTTPGet != nil {
			m.Spec.ReadinessProbe.HTTPGet.Port = r.getContainerPort(m)
		}
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					InitContainers: initContainer,
					NodeSelector:   m.Spec.NodeSelector,
					Containers: []corev1.Container{
						{
							Image:           m.Spec.Image,
							Name:            CSNAME,
							Env:             m.Spec.Envs,
							ImagePullPolicy: corev1.PullIfNotPresent,
							SecurityContext: &priviledged,
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: baseCodeDir,
									Name:      baseCodeVolume,
								},
							},
							Resources:      m.Spec.Resources,
							LivenessProbe:  m.Spec.LivenessProbe,
							ReadinessProbe: m.Spec.ReadinessProbe,
						},
					},
				},
			},
		},
	}
	// add volume pvc pr emptyDir
	if r.needDeployPVC(m.Spec.StorageName) {
		dataVolume := corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: m.Name,
		}
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: baseCodeVolume,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &dataVolume,
			},
		})
	} else {
		volumeQuantity, _ := resourcev1.ParseQuantity(m.Spec.StorageSize)
		dataVolume := corev1.EmptyDirVolumeSource{
			SizeLimit: &volumeQuantity,
		}
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: baseCodeVolume,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &dataVolume,
			},
		})
	}
	//https will be disabled no matter secret is provided or not. we also export same port here.
	for index, con := range dep.Spec.Template.Spec.Containers {
		if con.Name == CSNAME {
			if len(m.Spec.Command) != 0 {
				dep.Spec.Template.Spec.Containers[index].Command = m.Spec.Command
			} else if len(m.Spec.Args) != 0 {
				dep.Spec.Template.Spec.Containers[index].Args = m.Spec.Args
			}
			containerPort := r.getContainerPort(m)
			dep.Spec.Template.Spec.Containers[index].Ports = append(
				dep.Spec.Template.Spec.Containers[index].Ports, corev1.ContainerPort{
					ContainerPort: int32(containerPort.IntValue()),
					Name:          "http",
				})
		}
	}
	// Append ingress and egress limit
	dep.Spec.Template.Annotations = map[string]string{}
	if len(m.Spec.IngressBandwidth) != 0 {
		dep.Spec.Template.Annotations[IngressLimitKey] = m.Spec.IngressBandwidth
	}
	if len(m.Spec.EgressBandwidth) != 0 {
		dep.Spec.Template.Annotations[EgressLimitKey] = m.Spec.EgressBandwidth
	}
	// Set CodeServer instance as the owner of the Deployment.
	controllerutil.SetControllerReference(m, dep, r.Scheme)
	return dep
}

func (r *CodeServerReconciler) assembleBaseLxdEnvs(
	m *csv1alpha1.CodeServer, clientSecretPath string, additionalEnvs []corev1.EnvVar) []corev1.EnvVar {
	var instanceEnvs []corev1.EnvVar
	var gottyEnvs []string
	// 1. environments start from LAUNCHER will be added into pod env,
	// otherwise will be passed via LAUNCHER_INSTANCE_ENVS
	for _, env := range m.Spec.Envs {
		if strings.HasPrefix(env.Name, "LAUNCHER") {
			instanceEnvs = append(instanceEnvs, env)
		} else {
			// append to the lxd instance env
			gottyEnvs = append(gottyEnvs, fmt.Sprintf("%s=%s", env.Name, env.Value))
		}
	}
	for _, env := range additionalEnvs {
		if strings.HasPrefix(env.Name, "LAUNCHER") {
			instanceEnvs = append(instanceEnvs, env)
		} else {
			// append to the lxd instance env
			gottyEnvs = append(gottyEnvs, fmt.Sprintf("%s=%s", env.Name, env.Value))
		}
	}
	instanceEnvs = append(instanceEnvs,
		corev1.EnvVar{
			Name:  "LAUNCHER_INSTANCE_ENVS",
			Value: strings.Join(gottyEnvs, ","),
		})

	// 2. LAUNCHER_LXD_SERVER_ADDRESS, LAUNCHER_CLIENT_KEY_PATH, LAUNCHER_CLIENT_CERT_PATH
	instanceEnvs = append(instanceEnvs, corev1.EnvVar{
		Name:  "LAUNCHER_CLIENT_KEY_PATH",
		Value: path.Join(clientSecretPath, corev1.TLSPrivateKeyKey),
	}, corev1.EnvVar{
		Name:  "LAUNCHER_CLIENT_CERT_PATH",
		Value: path.Join(clientSecretPath, corev1.TLSCertKey),
	}, corev1.EnvVar{
		Name: "LAUNCHER_LXD_SERVER_ADDRESS",
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: "status.hostIP",
			},
		},
	})
	// 3. Launch start command, we need import all the envs before start service
	instanceEnvs = append(instanceEnvs, corev1.EnvVar{
		Name:  "LAUNCHER_START_COMMAND",
		Value: "systemctl import-environment && systemctl start gotty",
	})
	// 4. network bandwidth
	if len(m.Spec.IngressBandwidth) != 0 {
		instanceEnvs = append(instanceEnvs, corev1.EnvVar{
			Name:  "LAUNCHER_NETWORK_INGRESS",
			Value: m.Spec.IngressBandwidth,
		})
	}
	if len(m.Spec.EgressBandwidth) != 0 {
		instanceEnvs = append(instanceEnvs, corev1.EnvVar{
			Name:  "LAUNCHER_NETWORK_EGRESS",
			Value: m.Spec.EgressBandwidth,
		})
	}
	// 5. resource limit
	if value, ok := m.Spec.Resources.Requests[corev1.ResourceCPU]; ok {
		instanceEnvs = append(instanceEnvs, corev1.EnvVar{
			Name:  "LAUNCHER_CPU_RESOURCE",
			Value: value.String(),
		})
	}
	if value, ok := m.Spec.Resources.Requests[corev1.ResourceMemory]; ok {
		instanceEnvs = append(instanceEnvs, corev1.EnvVar{
			Name:  "LAUNCHER_MEMORY_RESOURCE",
			Value: value.String(),
		})
	}
	instanceEnvs = append(instanceEnvs, corev1.EnvVar{
		Name:  "LAUNCHER_STORAGE_POOL",
		Value: m.Spec.StorageName,
	}, corev1.EnvVar{
		Name:  "LAUNCHER_ROOT_SIZE",
		Value: m.Spec.StorageSize,
	})

	// 6. others
	instanceEnvs = append(instanceEnvs, corev1.EnvVar{
		Name:  "LAUNCHER_REMOVE_EXISTING",
		Value: "true",
	})
	return instanceEnvs
}

// deploymentForLxd returns an instance with gotty based terminal on lxd Deployment object for lxc launcher
func (r *CodeServerReconciler) deploymentForLxd(m *csv1alpha1.CodeServer) *appsv1.Deployment {
	reqLogger := r.Log.WithValues("namespace", m.Namespace, "name", m.Name)
	ls := appLabel(m.Name, m.Labels)
	baseProxyDir := r.getDefaultWorkSpace(m)
	baseProxyVolume := "code-server-workspace"
	replicas := int32(1)
	enablePriviledge := m.Spec.Privileged
	var additionalEnvs []corev1.EnvVar
	priviledged := corev1.SecurityContext{
		Privileged: enablePriviledge,
	}
	reqLogger.Info("lxd container doesn't support init containers.")
	ProxyPort := fmt.Sprintf("80:80,%d:%d", HttpPort, HttpPort)
	additionalEnvs = append(additionalEnvs, corev1.EnvVar{
		Name:  "GOTTY_PORT",
		Value: strconv.Itoa(HttpPort),
	}, corev1.EnvVar{
		Name:  "LAUNCHER_PROXY_PORT_PAIRS",
		Value: ProxyPort,
	})
	envs := r.assembleBaseLxdEnvs(m, baseProxyDir, additionalEnvs)
	//convert liveness or readiness probe
	if m.Spec.LivenessProbe != nil {
		if m.Spec.LivenessProbe.HTTPGet != nil {
			m.Spec.LivenessProbe.HTTPGet.Port = intstr.FromInt(HttpPort)
		}
	}
	if m.Spec.ReadinessProbe != nil {
		if m.Spec.ReadinessProbe.HTTPGet != nil {
			m.Spec.ReadinessProbe.HTTPGet.Port = intstr.FromInt(HttpPort)
		}
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					NodeSelector: m.Spec.NodeSelector,
					Containers: []corev1.Container{
						{
							Image:           m.Spec.Image,
							Name:            CSNAME,
							Env:             envs,
							Args:            []string{"launch", m.Name},
							ImagePullPolicy: corev1.PullIfNotPresent,
							SecurityContext: &priviledged,
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: baseProxyDir,
									Name:      baseProxyVolume,
								},
							},
							// pass resource requests to pod, actually, the resource will be consumed by lxd.
							Resources:      m.Spec.Resources,
							LivenessProbe:  m.Spec.LivenessProbe,
							ReadinessProbe: m.Spec.ReadinessProbe,
						},
					},
				},
			},
		},
	}
	// add secret volume for lxd cert
	secretVolume := corev1.Volume{
		Name: baseProxyVolume,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: r.Options.LxdClientSecretName,
			},
		},
	}
	dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, secretVolume)
	//https will be disabled no matter secret is provided or not. we also export same port here.
	for index, con := range dep.Spec.Template.Spec.Containers {
		if con.Name == CSNAME {
			dep.Spec.Template.Spec.Containers[index].Ports = append(
				dep.Spec.Template.Spec.Containers[index].Ports, corev1.ContainerPort{
					ContainerPort: HttpPort,
					Name:          "http",
				})
		}
	}
	// Append ingress and egress limit
	dep.Spec.Template.Annotations = map[string]string{}
	if len(m.Spec.IngressBandwidth) != 0 {
		dep.Spec.Template.Annotations[IngressLimitKey] = m.Spec.IngressBandwidth
	}
	if len(m.Spec.EgressBandwidth) != 0 {
		dep.Spec.Template.Annotations[EgressLimitKey] = m.Spec.EgressBandwidth
	}
	// Set CodeServer instance as the owner of the Deployment.
	controllerutil.SetControllerReference(m, dep, r.Scheme)
	return dep
}

func (r *CodeServerReconciler) getContainerPort(m *csv1alpha1.CodeServer) intstr.IntOrString {
	if len(m.Spec.ContainerPort) == 0 {
		return intstr.FromInt(HttpPort)
	}
	return intstr.Parse(m.Spec.ContainerPort)
}

func (r *CodeServerReconciler) getDefaultWorkSpace(m *csv1alpha1.CodeServer) string {
	if len(m.Spec.WorkspaceLocation) == 0 {
		return DefaultWorkspace
	} else {
		return m.Spec.WorkspaceLocation
	}
}

// newService function takes in a CodeServer object and returns a Service for that object.
func (r *CodeServerReconciler) newService(m *csv1alpha1.CodeServer) *corev1.Service {
	ls := appLabel(m.Name, m.Labels)
	ser := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: ls,
		},
	}
	ser.Spec.Ports = append(ser.Spec.Ports, corev1.ServicePort{
		Port:       HttpPort,
		Name:       "http",
		Protocol:   corev1.ProtocolTCP,
		TargetPort: r.getContainerPort(m),
	})
	// Set CodeServer instance as the owner of the Service.
	controllerutil.SetControllerReference(m, ser, r.Scheme)
	return ser
}

func (r *CodeServerReconciler) needDeployPVC(storageName string) bool {
	if storageName == StorageEmptyDir || len(storageName) == 0 {
		return false
	}
	return true
}

// newPVC function takes in a CodeServer object and returns a PersistentVolumeClaim for that object.
func (r *CodeServerReconciler) newPVC(m *csv1alpha1.CodeServer) (*corev1.PersistentVolumeClaim, error) {
	pvcQuantity, err := resourcev1.ParseQuantity(m.Spec.StorageSize)
	if err != nil {
		return nil, err
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        m.Name,
			Namespace:   m.Namespace,
			Annotations: m.Spec.StorageAnnotations,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &m.Spec.StorageName,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: pvcQuantity,
				},
			},
		},
	}
	// Set CodeServer instance as the owner of the pvc.
	controllerutil.SetControllerReference(m, pvc, r.Scheme)
	return pvc, nil
}

// NewIngress function takes in a CodeServer object and returns an ingress for that object.
func (r *CodeServerReconciler) NewIngress(m *csv1alpha1.CodeServer) *ingressv1.Ingress {
	nginxClass := "nginx"
	pathType := ingressv1.PathTypePrefix
	httpValue := ingressv1.HTTPIngressRuleValue{
		Paths: []ingressv1.HTTPIngressPath{
			{
				Path:     "/",
				PathType: &pathType,
				Backend: ingressv1.IngressBackend{
					Service: &ingressv1.IngressServiceBackend{
						Name: m.Name,
						Port: ingressv1.ServiceBackendPort{
							Number: HttpPort,
						},
					},
				},
			},
		},
	}
	ingress := &ingressv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf(TerminalIngress, m.Name),
			Namespace:   m.Namespace,
			Annotations: r.annotationsForIngress(),
		},
		Spec: ingressv1.IngressSpec{
			IngressClassName: &nginxClass,
			Rules: []ingressv1.IngressRule{
				{
					Host: fmt.Sprintf("%s.%s", m.Spec.Subdomain, r.Options.DomainName),
					IngressRuleValue: ingressv1.IngressRuleValue{
						HTTP: &httpValue,
					},
				},
			},
		},
	}
	ingress.Spec.TLS = []ingressv1.IngressTLS{
		{
			Hosts:      []string{fmt.Sprintf("%s.%s", m.Spec.Subdomain, r.Options.DomainName)},
			SecretName: r.Options.HttpsSecretName,
		},
	}
	// Set CodeServer instance as the owner of the ingress.
	controllerutil.SetControllerReference(m, ingress, r.Scheme)
	return ingress
}

func (r *CodeServerReconciler) annotationsForIngress() map[string]string {
	annotation := map[string]string{}
	// currently, we don't enable https for backend
	//annotation["nginx.ingress.kubernetes.io/secure-backends"] = "true"
	//annotation["nginx.ingress.kubernetes.io/backend-protocol"] = "HTTPS"

	annotation["nginx.ingress.kubernetes.io/proxy-read-timeout"] = "1800"
	annotation["nginx.ingress.kubernetes.io/proxy-send-timeout"] = "1800"
	return annotation
}

// appLabel returns the labels for selecting the resources
// belonging to the given instance name.
func appLabel(name string, labels map[string]string) map[string]string {
	label := map[string]string{"app": "codeserver", "cs_name": name}
	if labels != nil && len(labels) > 0 {
		for k, v := range labels {
			label[k] = v
		}
	}
	return label
}

// NewStateCondition creates a new code server condition.
func NewStateCondition(conditionType csv1alpha1.ServerConditionType, reason string, message map[string]string,
	status corev1.ConditionStatus) csv1alpha1.ServerCondition {
	return csv1alpha1.ServerCondition{
		Type:               conditionType,
		Status:             status,
		LastUpdateTime:     metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// HasCondition checks whether the instance has the specified condition
func HasCondition(status csv1alpha1.CodeServerStatus, condType csv1alpha1.ServerConditionType) bool {
	for _, condition := range status.Conditions {
		if condition.Type == condType && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// MissingCondition checks whether the instance miss the specified condition
func MissingCondition(status csv1alpha1.CodeServerStatus, condType csv1alpha1.ServerConditionType) bool {
	for _, condition := range status.Conditions {
		if condition.Type == condType {
			return false
		}
	}
	return true
}

func HasDeploymentCondition(status appsv1.DeploymentStatus, condType appsv1.DeploymentConditionType) bool {
	for _, condition := range status.Conditions {
		if condition.Type == condType && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// GetCondition gets the condition with specified condition type
func GetCondition(status csv1alpha1.CodeServerStatus, condType csv1alpha1.ServerConditionType) *csv1alpha1.ServerCondition {
	for _, condition := range status.Conditions {
		if condition.Type == condType {
			return &condition
		}
	}
	return nil
}

// SetCondition updates the code server status with provided condition
func SetCondition(status *csv1alpha1.CodeServerStatus, condition csv1alpha1.ServerCondition) bool {

	currentCond := GetCondition(*status, condition.Type)

	// Do nothing if condition doesn't change
	if currentCond != nil && currentCond.Status == condition.Status && currentCond.Reason == condition.Reason {
		return false
	}

	// Do not update lastTransitionTime if the status of the condition doesn't change.
	if currentCond != nil && currentCond.Status == condition.Status {
		condition.LastTransitionTime = currentCond.LastTransitionTime
	}

	// Append the updated condition to the job status
	newConditions := filterOutCondition(status, condition)
	status.Conditions = append(newConditions, condition)
	return true
}

func filterOutCondition(states *csv1alpha1.CodeServerStatus, currentCondition csv1alpha1.ServerCondition) []csv1alpha1.ServerCondition {

	var newConditions []csv1alpha1.ServerCondition
	for _, condition := range states.Conditions {
		//Filter out the same condition
		if condition.Type == currentCondition.Type {
			continue
		}

		if currentCondition.Type == csv1alpha1.ServerCreated {
			break
		}

		if currentCondition.Type == csv1alpha1.ServerInactive || currentCondition.Type == csv1alpha1.ServerRecycled ||
			currentCondition.Type == csv1alpha1.ServerErrored {
			if currentCondition.Status == corev1.ConditionTrue && condition.Type == csv1alpha1.ServerReady {
				condition.Status = corev1.ConditionFalse
				condition.LastUpdateTime = metav1.Now()
				condition.LastTransitionTime = metav1.Now()
			}
		}

		if currentCondition.Type == csv1alpha1.ServerReady && currentCondition.Status == corev1.ConditionTrue {
			if condition.Type == csv1alpha1.ServerRecycled || condition.Type == csv1alpha1.ServerInactive ||
				condition.Type == csv1alpha1.ServerErrored {
				condition.Status = corev1.ConditionFalse
				condition.LastUpdateTime = metav1.Now()
				condition.LastTransitionTime = metav1.Now()
				condition.Message = map[string]string{}
				condition.Reason = "code server becomes ready"
			}
		}
		newConditions = append(newConditions, condition)
	}
	return newConditions
}

func needUpdatePVC(old, new *corev1.PersistentVolumeClaim) bool {
	return *old.Spec.StorageClassName != *new.Spec.StorageClassName ||
		!equality.Semantic.DeepEqual(old.Spec.Resources, new.Spec.Resources)
}

func needUpdateService(old, new *corev1.Service) bool {
	return !equality.Semantic.DeepEqual(old.Spec.Ports, new.Spec.Ports) ||
		!equality.Semantic.DeepEqual(old.Spec.Selector, new.Spec.Selector)
}

func needUpdateDeployment(old, new *appsv1.Deployment) bool {
	return !equality.Semantic.DeepEqual(old.Spec.Template.Spec.Volumes, new.Spec.Template.Spec.Volumes) ||
		!equality.Semantic.DeepEqual(old.Spec.Template.Spec.Containers, new.Spec.Template.Spec.Containers)
}

func (r *CodeServerReconciler) SetupWithManager(mgr ctrl.Manager, maxConcurrency int) error {
	options := controller.Options{
		MaxConcurrentReconciles: maxConcurrency,
	}
	//watch codeserver, server, ingress, pvc and deployment.
	return ctrl.NewControllerManagedBy(mgr).
		For(&csv1alpha1.CodeServer{}).Owns(&corev1.Service{}).
		Owns(&ingressv1.Ingress{}).Owns(&appsv1.Deployment{}).Owns(&corev1.PersistentVolumeClaim{}).WithOptions(options).
		Complete(r)
}
