//go:build gpu && cgo && linux && amd64

package goinfer

import (
	"fmt"

	"github.com/oliverbestmann/webgpu/wgpu"
	_ "github.com/townsendmerino/goinfer/backend/webgpu"
)

const webGPUBackendPin = "github.com/sh4869221b/goinfer/backend/webgpu@994cbf5ee38dd8dcc612aad6e6803ac79f772f3b"

// WGPUInstanceBackend_Vulkan in the pinned wgpu-native header.
const vulkanBackends wgpu.InstanceBackend = 1 << 0

func webGPUAdapter() (name, driver string, err error) {
	instance := wgpu.CreateInstance(&wgpu.InstanceDescriptor{Backends: vulkanBackends})
	defer instance.Release()
	adapters := instance.EnumerateAdapters(&wgpu.InstanceEnumerateAdapterOptons{Backends: vulkanBackends})
	for _, adapter := range adapters {
		defer adapter.Release()
	}
	// The fixed goinfer factory chooses its own adapter. A single visible
	// Vulkan candidate and the loaded model's Vulkan path identify that device.
	if len(adapters) != 1 {
		return "", "", fmt.Errorf("%w: expected one visible Vulkan adapter, found %d", ErrWebGPUResidency, len(adapters))
	}
	info := adapters[0].GetInfo()
	if info.BackendType != wgpu.BackendTypeVulkan || info.AdapterType != wgpu.AdapterTypeDiscreteGPU || info.VendorId != 0x1002 {
		return "", "", fmt.Errorf("%w: Vulkan adapter must be a discrete AMD GPU (device %04x:%04x, type %s)", ErrWebGPUResidency, info.VendorId, info.DeviceId, info.AdapterType)
	}
	return info.Device, info.Description, nil
}
