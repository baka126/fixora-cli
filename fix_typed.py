import re

with open("internal/kube/typed.go", "r") as f:
    content = f.read()

content = content.replace(
"""	obj, err := withRetry(func() (*dynamic.ResourceInterface, error) {
		res := c.dynamicResource(gvr, namespace)
		_, getErr := res.Get(ctx, name, metav1.GetOptions{})
		return &res, getErr
	})
	if err != nil {
		return nil, err
	}
	realObj, _ := c.dynamicResource(gvr, namespace).Get(ctx, name, metav1.GetOptions{})
	return realObj.Object, nil""",
"""	obj, err := withRetry(func() (*unstructured.Unstructured, error) {
		res := c.dynamicResource(gvr, namespace)
		return res.Get(ctx, name, metav1.GetOptions{})
	})
	if err != nil {
		return nil, err
	}
	return obj.Object, nil"""
)

content = content.replace(
"""		list, err := withRetry(func() (*dynamic.ResourceInterface, error) {
			res := c.dynamicResource(gvr, namespaceForTyped(namespace, allNS))
			_, listErr := res.List(ctx, listOpts)
			return &res, listErr
		})
		if err != nil {
			return nil, err
		}
		realList, _ := c.dynamicResource(gvr, namespaceForTyped(namespace, allNS)).List(ctx, listOpts)
		for _, item := range realList.Items {
			items = append(items, item.Object)
		}
		continueToken = realList.GetContinue()""",
"""		list, err := withRetry(func() (*unstructured.UnstructuredList, error) {
			res := c.dynamicResource(gvr, namespaceForTyped(namespace, allNS))
			return res.List(ctx, listOpts)
		})
		if err != nil {
			return nil, err
		}
		for _, item := range list.Items {
			items = append(items, item.Object)
		}
		continueToken = list.GetContinue()"""
)

content = content.replace(
"""	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema\"""",
"""	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema\""""
)

with open("internal/kube/typed.go", "w") as f:
    f.write(content)
