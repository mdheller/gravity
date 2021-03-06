/*
Copyright 2018 Gravitational, Inc.

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

package kubernetes

import (
	"github.com/gravitational/gravity/lib/utils"

	"github.com/gravitational/rigging"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DeletePods deletes all pods matching selector using provided client
func DeletePods(client *kubernetes.Clientset, namespace string, selector map[string]string) error {
	return rigging.ConvertError(client.Core().Pods(namespace).DeleteCollection(
		nil, metav1.ListOptions{
			LabelSelector: utils.MakeSelector(selector).String(),
		}))
}
