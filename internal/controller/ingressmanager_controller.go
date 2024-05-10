/*
Copyright 2024.

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

package controller

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	operatorcodehorsecomv1beta1 "codehorse.com/ingress-manager-operator/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
)

// IngressManagerReconciler reconciles a IngressManager object
type IngressManagerReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	IngressMangerQueue map[string]*operatorcodehorsecomv1beta1.IngressManager
	Lock               sync.RWMutex
	Tickers            []*time.Ticker
	Wg                 sync.WaitGroup
}

//+kubebuilder:rbac:groups=operator.codehorse.com,resources=ingressmanagers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=operator.codehorse.com,resources=ingressmanagers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=operator.codehorse.com,resources=ingressmanagers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the IngressManager object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *IngressManagerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)
	ingressManagerK8s := operatorcodehorsecomv1beta1.NewIngressManager()
	err := r.Client.Get(ctx, req.NamespacedName, ingressManagerK8s)
	if err != nil {
		if errors.IsNotFound(err) {
			operatorcodehorsecomv1beta1.L().Error().Msgf("[%s]没有找到", req.NamespacedName.Name)
			// 从队列中删除
			r.DeleteQueue(ingressManagerK8s)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, err
	}
	if ingressManager, ok := r.IngressMangerQueue[ingressManagerK8s.Name]; ok {
		if reflect.DeepEqual(ingressManager.Spec, ingressManagerK8s.Spec) {
			operatorcodehorsecomv1beta1.L().Error().Msgf("[%s]没有发生变化", req.NamespacedName.Name)
			return ctrl.Result{}, fmt.Errorf("[%s]没有发生变化", req.NamespacedName.Name)
		}
	}
	// 添加到队列
	r.AddQueue(ingressManagerK8s)
	return ctrl.Result{}, nil
}

func (r *IngressManagerReconciler) AddQueue(ingressManager *operatorcodehorsecomv1beta1.IngressManager) {
	if r.IngressMangerQueue == nil {
		r.IngressMangerQueue = make(map[string]*operatorcodehorsecomv1beta1.IngressManager)
	}
	r.IngressMangerQueue[ingressManager.Name] = ingressManager
	r.StopLoopTask()
	go r.RunLoopTask()
}

func (r *IngressManagerReconciler) DeleteQueue(ingressManager *operatorcodehorsecomv1beta1.IngressManager) {
	delete(r.IngressMangerQueue, ingressManager.Name)
	r.StopLoopTask()
	go r.RunLoopTask()
}

func (r *IngressManagerReconciler) RunLoopTask() {
	for _, ingressManager := range r.IngressMangerQueue {
		if !ingressManager.Spec.Active {
			operatorcodehorsecomv1beta1.L().Info().Msgf("[%s]没有启动", ingressManager.Name)
			ingressManager.Status.Active = false
			// 更新状态
			r.UpdateStatus(ingressManager)
			continue
		}
		// 更新状态
		delayTime := r.GetDelaySeconds(ingressManager.Spec.StartTime)
		if delayTime.Hours() >= 1 {
			operatorcodehorsecomv1beta1.L().Info().Msgf("[%s]在%.1f小时后执行", ingressManager.Name, delayTime.Hours())
		} else {
			operatorcodehorsecomv1beta1.L().Info().Msgf("[%s]在%.1f分钟后执行", ingressManager.Name, delayTime.Minutes())
		}
		ingressManager.Status.Active = true
		ingressManager.Status.NextTime = r.GetNextTime(delayTime.Seconds()).Unix()
		r.UpdateStatus(ingressManager)
		// 开始计时
		ticker := time.NewTicker(delayTime)
		r.Tickers = append(r.Tickers, ticker)
		r.Wg.Add(1)
		go func(ingressManager *operatorcodehorsecomv1beta1.IngressManager) {
			defer r.Wg.Done()
			// 处理业务逻辑
			for {
				<-ticker.C
				// 重置计时器
				ticker.Reset(time.Minute * time.Duration(ingressManager.Spec.Period))
				ingressManager.Status.Active = true
				ingressManager.Status.NextTime = r.GetNextTime(float64(ingressManager.Spec.Period * 60)).Unix()
				// 根据指定Service创建Ingress
				err := r.CreateIngressFromService(ingressManager)
				if err != nil {
					operatorcodehorsecomv1beta1.L().Error().Msgf("[%s]任务执行失败, 原因: %s", ingressManager.Name, err.Error())
					ingressManager.Status.LastResult = fmt.Sprintf("[%s]任务执行失败, 原因: %s", ingressManager.Name, err.Error())
				} else {
					operatorcodehorsecomv1beta1.L().Error().Msgf("[%s]任务执行成功", ingressManager.Name)
					ingressManager.Status.LastResult = fmt.Sprintf("[%s]任务执行成功", ingressManager.Name)
				}
				// 更新状态
				r.UpdateStatus(ingressManager)
			}

		}(ingressManager)
	}
	r.Wg.Wait()
}

func (r *IngressManagerReconciler) CreateIngressFromService(ingressManager *operatorcodehorsecomv1beta1.IngressManager) error {
	svcNamespaceName := types.NamespacedName{
		Namespace: ingressManager.Spec.Service.Namespace,
		Name:      ingressManager.Spec.Service.Name,
	}
	service := &corev1.Service{}
	err := r.Client.Get(context.TODO(), svcNamespaceName, service)
	if err != nil {
		if errors.IsNotFound(err) {
			operatorcodehorsecomv1beta1.L().Error().Msgf("[%s]查找不到该service", svcNamespaceName.Name)
			return err
		}
		operatorcodehorsecomv1beta1.L().Error().Msgf("[%s]查找该service出错,原因: [%s]", svcNamespaceName.Name, err.Error())
		return err
	}
	ingressNamespaceName := types.NamespacedName{
		Namespace: ingressManager.Spec.Ingress.Namespace,
		Name:      ingressManager.Spec.Ingress.Name,
	}
	ingress := &netv1.Ingress{}
	err = r.Client.Get(context.TODO(), ingressNamespaceName, ingress)
	if err != nil {
		if errors.IsNotFound(err) {
			// 创建Ingress
			ingress := &netv1.Ingress{}
			ingress.Kind = "Ingress"
			ingress.APIVersion = "networking.k8s.io/v1"
			ingress.Namespace = ingressManager.Spec.Ingress.Namespace
			ingress.Name = ingressManager.Spec.Ingress.Name
			pathType := netv1.PathTypeExact
			ingress.Spec = netv1.IngressSpec{
				IngressClassName: &ingressManager.Spec.Ingress.ClassName,
				Rules: []netv1.IngressRule{
					{
						Host: ingressManager.Spec.Ingress.Host,
						IngressRuleValue: netv1.IngressRuleValue{
							HTTP: &netv1.HTTPIngressRuleValue{
								Paths: []netv1.HTTPIngressPath{
									{
										Path:     ingressManager.Spec.Ingress.Path,
										PathType: &pathType,
										Backend: netv1.IngressBackend{
											Service: &netv1.IngressServiceBackend{
												Name: ingressManager.Spec.Service.Name,
												Port: netv1.ServiceBackendPort{
													Number: service.Spec.Ports[0].Port,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}
			err = r.Client.Create(context.TODO(), ingress)
			if err != nil {
				return err
			}
			return nil
		}
		operatorcodehorsecomv1beta1.L().Error().Msgf("[%s]查找该ingress出错,原因: [%s]", ingressNamespaceName.Name, err.Error())
		return err
	}
	return nil
}

func (r *IngressManagerReconciler) StopLoopTask() {
	for _, ticker := range r.Tickers {
		if ticker != nil {
			ticker.Stop()
		}
	}
}

func (r *IngressManagerReconciler) UpdateStatus(ingressManager *operatorcodehorsecomv1beta1.IngressManager) {
	r.Lock.Lock()
	defer r.Lock.Unlock()
	namespaceName := types.NamespacedName{
		Namespace: ingressManager.Namespace,
		Name:      ingressManager.Name,
	}
	ingressManagerK8s := operatorcodehorsecomv1beta1.NewIngressManager()
	err := r.Client.Get(context.TODO(), namespaceName, ingressManagerK8s)
	if err != nil {
		operatorcodehorsecomv1beta1.L().Error().Msgf("获取k8s中[%s]失败", namespaceName.Name)
		return
	}
	ingressManagerK8s.Status = ingressManager.Status
	err = r.Client.Status().Update(context.TODO(), ingressManagerK8s)
	if err != nil {
		operatorcodehorsecomv1beta1.L().Error().Msgf("更新k8s中[%s]状态失败", namespaceName.Name)
		return
	}
}

func (r *IngressManagerReconciler) GetDelaySeconds(startTime string) time.Duration {
	times := strings.Split(startTime, ":")
	expectedHour, _ := strconv.Atoi(times[0])
	expectedMin, _ := strconv.Atoi(times[1])
	now := time.Now().Truncate(time.Second)
	todayDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrowDate := todayDate.Add(time.Hour * time.Duration(24))
	curDuration := time.Hour*time.Duration(now.Hour()) + time.Minute*time.Duration(now.Minute())
	expectedDuration := time.Hour*time.Duration(expectedHour) + time.Minute*time.Duration(expectedMin)
	var seconds int
	if curDuration >= expectedDuration {
		seconds = int(tomorrowDate.Add(expectedDuration).Sub(now).Seconds())
	} else {
		seconds = int(todayDate.Add(expectedDuration).Sub(now).Seconds())
	}
	return time.Second * time.Duration(seconds)
}

func (r *IngressManagerReconciler) GetNextTime(seconds float64) time.Time {
	return time.Now().Add(time.Second * time.Duration(seconds))
}

// SetupWithManager sets up the controller with the Manager.
func (r *IngressManagerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&operatorcodehorsecomv1beta1.IngressManager{}).
		Complete(r)
}
