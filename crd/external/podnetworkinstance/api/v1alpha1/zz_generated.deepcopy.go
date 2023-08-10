//go:build !ignore_autogenerated
// +build !ignore_autogenerated

// Code generated by controller-gen. DO NOT EDIT.

package v1alpha1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PodNetworkInstance) DeepCopyInto(out *PodNetworkInstance) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PodNetworkInstance.
func (in *PodNetworkInstance) DeepCopy() *PodNetworkInstance {
	if in == nil {
		return nil
	}
	out := new(PodNetworkInstance)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *PodNetworkInstance) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PodNetworkInstanceList) DeepCopyInto(out *PodNetworkInstanceList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]PodNetworkInstance, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PodNetworkInstanceList.
func (in *PodNetworkInstanceList) DeepCopy() *PodNetworkInstanceList {
	if in == nil {
		return nil
	}
	out := new(PodNetworkInstanceList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *PodNetworkInstanceList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PodNetworkInstanceSpec) DeepCopyInto(out *PodNetworkInstanceSpec) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PodNetworkInstanceSpec.
func (in *PodNetworkInstanceSpec) DeepCopy() *PodNetworkInstanceSpec {
	if in == nil {
		return nil
	}
	out := new(PodNetworkInstanceSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PodNetworkInstanceStatus) DeepCopyInto(out *PodNetworkInstanceStatus) {
	*out = *in
	if in.PodIPAddresses != nil {
		in, out := &in.PodIPAddresses, &out.PodIPAddresses
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PodNetworkInstanceStatus.
func (in *PodNetworkInstanceStatus) DeepCopy() *PodNetworkInstanceStatus {
	if in == nil {
		return nil
	}
	out := new(PodNetworkInstanceStatus)
	in.DeepCopyInto(out)
	return out
}
