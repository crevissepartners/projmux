//go:build darwin && cgo

package systemstatus

/*
#include <mach/mach.h>
#include <stdint.h>
#include <sys/sysctl.h>

static int projmux_cpu_sample(uint64_t *total, uint64_t *idle) {
	host_cpu_load_info_data_t info;
	mach_msg_type_number_t count = HOST_CPU_LOAD_INFO_COUNT;
	kern_return_t result = host_statistics(
		mach_host_self(),
		HOST_CPU_LOAD_INFO,
		(host_info_t)&info,
		&count
	);
	if (result != KERN_SUCCESS || count < HOST_CPU_LOAD_INFO_COUNT) {
		return 0;
	}

	*total = 0;
	for (int state = 0; state < CPU_STATE_MAX; state++) {
		*total += (uint64_t)info.cpu_ticks[state];
	}
	*idle = (uint64_t)info.cpu_ticks[CPU_STATE_IDLE];
	return *idle <= *total;
}

static int projmux_memory_sample(
	uint64_t *total_bytes,
	uint64_t *page_size,
	uint64_t *free_pages,
	uint64_t *inactive_pages
) {
	size_t total_size = sizeof(*total_bytes);
	if (sysctlbyname("hw.memsize", total_bytes, &total_size, NULL, 0) != 0 ||
		total_size != sizeof(*total_bytes) || *total_bytes == 0) {
		return 0;
	}

	host_t host = mach_host_self();
	vm_size_t native_page_size = 0;
	if (host_page_size(host, &native_page_size) != KERN_SUCCESS || native_page_size == 0) {
		return 0;
	}

	vm_statistics64_data_t info;
	mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
	kern_return_t result = host_statistics64(
		host,
		HOST_VM_INFO64,
		(host_info64_t)&info,
		&count
	);
	if (result != KERN_SUCCESS || count < HOST_VM_INFO64_REV0_COUNT) {
		return 0;
	}

	*page_size = (uint64_t)native_page_size;
	*free_pages = (uint64_t)info.free_count;
	*inactive_pages = (uint64_t)info.inactive_count;
	return 1;
}
*/
import "C"

import (
	"math"
	"math/bits"
)

func (s Sampler) Sample() Metrics {
	metrics := Metrics{}
	if current, ok := darwinCPUSample(); ok {
		metrics.CPUPercent = s.sampleCPU(current)
	}
	metrics.MemoryPercent = darwinMemorySample()
	return metrics
}

func darwinCPUSample() (cpuSample, bool) {
	var total, idle C.uint64_t
	if C.projmux_cpu_sample(&total, &idle) == 0 {
		return cpuSample{}, false
	}
	return cpuSample{Total: uint64(total), Idle: uint64(idle)}, true
}

func darwinMemorySample() *int {
	var totalBytes, pageSize, freePages, inactivePages C.uint64_t
	if C.projmux_memory_sample(&totalBytes, &pageSize, &freePages, &inactivePages) == 0 {
		return nil
	}
	return darwinMemoryPercent(
		uint64(totalBytes),
		uint64(pageSize),
		uint64(freePages),
		uint64(inactivePages),
	)
}

func darwinMemoryPercent(totalBytes, pageSize, freePages, inactivePages uint64) *int {
	if totalBytes == 0 || pageSize == 0 {
		return nil
	}
	availablePages, carry := bits.Add64(freePages, inactivePages, 0)
	if carry != 0 {
		return nil
	}
	high, availableBytes := bits.Mul64(availablePages, pageSize)
	if high != 0 || availableBytes > totalBytes {
		return nil
	}
	percent := int(math.Round(float64(totalBytes-availableBytes) * 100 / float64(totalBytes)))
	percent = min(max(percent, 0), 100)
	return &percent
}
