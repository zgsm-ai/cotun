package chserver

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

/**
 * PortAllocator manages available ports allocation
 * @description
 * - Maintains port pool with unused/allocated/occupied states
 * - Provides thread-safe port allocation
 * - Supports port lookup by clientId and appName
 */
type PortAllocator struct {
	mu      sync.Mutex
	names   map[string]*PortAllocation // key: "clientId-appName" -> alloc
	ports   map[int]*PortAllocation    // port number -> alloc
	minPort int
	maxPort int
}

/**
 * NewPortAllocator creates a new port allocator instance
 * @param {int} minPort - Start port of allocation range
 * @param {int} maxPort - End port of allocation range
 * @returns {*PortAllocator} Initialized port allocator
 */
func NewPortAllocator(minPort, maxPort int) *PortAllocator {
	return &PortAllocator{
		names:   make(map[string]*PortAllocation),
		ports:   make(map[int]*PortAllocation),
		minPort: minPort,
		maxPort: maxPort,
	}
}

/**
 * AllocatePort assigns a new port mapping
 * @param {string} clientId - Client identifier
 * @param {string} appName - Application name
 * @param {int} clientPort - Client port number
 * @returns {PortAllocation, error} Allocation details or error
 */
func (pa *PortAllocator) AllocatePort(clientId, userId, appName string, clientPort int) (PortAllocation, error) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	key := clientId + "-" + userId + "-" + appName
	if alloc, exists := pa.names[key]; exists {
		alloc.ClientPort = clientPort
		alloc.Status = Allocated
		return *alloc, nil
	}

	for port := pa.minPort; port <= pa.maxPort; port++ {
		alloc, exists := pa.ports[port]
		if !exists || alloc.Status != Allocated {
			alloc = &PortAllocation{
				ClientId:    clientId,
				UserId:      userId,
				AppName:     appName,
				ClientPort:  clientPort,
				MappingPort: port,
				StartTime:   time.Now().Local(),
				Status:      Allocated,
			}
			pa.names[key] = alloc
			pa.ports[port] = alloc
			return *alloc, nil
		}
	}

	return PortAllocation{}, errors.New("no available ports")
}

/**
 *	应用端口
 *	新版本cotun客户端，建立连接时会调用该接口应用端口
 */
func (pa *PortAllocator) ApplyPort(clientId, userId, appName string, clientPort, mappingPort int) (*PortAllocation, error) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	key := clientId + "-" + userId + "-" + appName
	if alloc, exists := pa.names[key]; exists {
		if clientPort != alloc.ClientPort || mappingPort != alloc.MappingPort {
			return nil, errors.New("port conflict")
		}
		alloc.Status = Connected
		return alloc, nil
	}
	if _, exists := pa.ports[mappingPort]; exists {
		return nil, errors.New("port conflict")
	}

	alloc := &PortAllocation{
		ClientId:    clientId,
		UserId:      userId,
		AppName:     appName,
		ClientPort:  clientPort,
		MappingPort: mappingPort,
		StartTime:   time.Now().Local(),
		Status:      Connected,
	}
	pa.names[key] = alloc
	pa.ports[mappingPort] = alloc
	return alloc, nil
}

/**
 *	应用已经分配的端口
 *	旧版本的cotun客户端，建立连接时已经预分配了mappingPort，会直接指定端口对
 */
func (pa *PortAllocator) ApplyAllocatedPort(clientPort, mappingPort int) (*PortAllocation, error) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	alloc, exists := pa.ports[mappingPort]
	if !exists {
		return nil, fmt.Errorf("port [%d->%d] not exist", clientPort, mappingPort)
	}
	if clientPort != alloc.ClientPort {
		return nil, fmt.Errorf("port [%d] conflict: [%d ~ %d]", alloc.MappingPort, clientPort, alloc.ClientPort)
	}
	if alloc.Status != Allocated {
		return nil, fmt.Errorf("mapping port [%d] already used", alloc.MappingPort)
	}
	alloc.Status = Connected
	return alloc, nil
}

/**
 * ReleasePort frees allocated port
 * @param {string} clientId - Client identifier
 * @param {string} appName - Application name (empty releases all client ports)
 */
func (pa *PortAllocator) ReleasePort(clientId, userId, appName string) []PortAllocation {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	allocs := []PortAllocation{}
	if appName == "" || userId == "" {
		for key, alloc := range pa.names {
			if alloc.Status == Freed {
				continue
			}
			if alloc.ClientId == clientId {
				alloc.Status = Freed
				allocs = append(allocs, *alloc)
				delete(pa.names, key)
			}
		}
		return allocs
	}

	key := clientId + "-" + userId + "-" + appName
	if alloc, exists := pa.names[key]; exists {
		alloc.Status = Freed
		allocs = append(allocs, *alloc)
		delete(pa.names, key)
	}
	return allocs
}

/**
 * LookupPort finds allocated port for client/app
 * @param {string} clientId - Client identifier
 * @param {string} appName - Application name
 * @returns {int, error} Port number or error if not found
 */
func (pa *PortAllocator) LookupPort(clientId, userId, appName string) (*PortAllocation, error) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	key := clientId + "-" + userId + "-" + appName
	if alloc, exists := pa.names[key]; exists {
		if alloc.Status == Freed {
			return nil, errors.New("port mapping not found")
		}
		return alloc, nil
	}
	return nil, errors.New("port mapping not found")
}

func (pa *PortAllocator) QueryPorts(clientId, userId, appName string) []PortAllocation {
	ports := []PortAllocation{}

	pa.mu.Lock()
	defer pa.mu.Unlock()

	for _, alloc := range pa.names {
		if clientId != "" && clientId != alloc.ClientId {
			continue
		}
		if userId != "" && userId != alloc.UserId {
			continue
		}
		if appName != "" && appName != alloc.AppName {
			continue
		}
		ports = append(ports, *alloc)
	}
	return ports
}
