package main

//#include "common.h"
//#include "netfilter.h"
//#include "conntrack.h"
//#include "netlogger.h"
//#cgo LDFLAGS: -lnetfilter_queue -lnfnetlink -lnetfilter_conntrack -lnetfilter_log
import "C"

import "os"
import "fmt"
import "net"
import "time"
import "sync"
import "bufio"
import "unsafe"
import "encoding/binary"
import "github.com/google/gopacket"
import "github.com/google/gopacket/layers"
import "github.com/untangle/packetd/support"
import "github.com/untangle/packetd/example"
import "github.com/untangle/packetd/classify"
import "github.com/untangle/packetd/geoip"
import "github.com/untangle/packetd/certcache"
import "github.com/untangle/packetd/restd"

/*---------------------------------------------------------------------------*/

/*
 * The childsync is used to give the main process something to watch while
 * waiting for all of the goroutine children to finish execution and cleanup.
 * To give C child functions access we export go_child_startup and goodbye
 * functions. For children in normal go packages, we pass the WaitGroup
 * directly to the goroutine.
 */
var childsync sync.WaitGroup

/*---------------------------------------------------------------------------*/
func main() {
	var lastmin int
	var counter int

	support.Startup()
	C.common_startup()

	support.LogMessage("Untangle Packet Daemon Version %s\n", "1.00")

	go C.netfilter_thread()
	go C.conntrack_thread()
	go C.netlogger_thread()

	// ********** Call all plugin startup functions here

	go example.Plugin_Startup(&childsync)
	go classify.Plugin_Startup(&childsync)
	go geoip.Plugin_Startup(&childsync)
	go certcache.Plugin_Startup(&childsync)

	// Start REST HTTP daemon
	go restd.StartRestDaemon()

	// ********** End of plugin startup functions

	ch := make(chan string)
	go func(ch chan string) {
		reader := bufio.NewReader(os.Stdin)
		for {
			s, err := reader.ReadString('\n')
			if err != nil {
				close(ch)
				return
			}
			ch <- s
		}
		close(ch)
	}(ch)

	support.LogMessage("RUNNING ON CONSOLE - HIT ENTER TO EXIT\n")

stdinloop:
	for {
		shutdown := C.get_shutdown_flag()
		if shutdown != 0 {
			break
		}
		select {
		case stdin, ok := <-ch:
			if !ok {
				break stdinloop
			} else {
				support.LogMessage("Console input detected - Application shutting down\n")
				_ = stdin
				break stdinloop
			}
		case <-time.After(1 * time.Second):
			current := time.Now()
			if current.Minute() != lastmin {
				lastmin = current.Minute()
				counter++
				support.LogMessage("Calling perodic conntrack dump %d\n", counter)
				C.conntrack_dump()
				support.CleanConntrackTable()
			}
		}
	}

	// ********** Call all plugin goodbye functions here

	go example.Plugin_Goodbye(&childsync)
	go classify.Plugin_Goodbye(&childsync)
	go geoip.Plugin_Goodbye(&childsync)
	go certcache.Plugin_Goodbye(&childsync)

	// ********** End of plugin goodbye functions

	C.netfilter_goodbye()
	C.conntrack_goodbye()
	C.netlogger_goodbye()
	childsync.Wait()
}

/*---------------------------------------------------------------------------*/
//export go_netfilter_callback
func go_netfilter_callback(mark C.int, data *C.uchar, size C.int) int32 {

	// ***** this version creates a Go copy of the buffer = SLOWER
	// buffer := C.GoBytes(unsafe.Pointer(data),size)

	// ***** this version creates a Go pointer to the buffer = FASTER
	buffer := (*[0xFFFF]byte)(unsafe.Pointer(data))[:int(size):int(size)]

	// convert the C integer to a Go integer
	length := int(size)

	// get the existing mark on the packet
	var pmark int32 = int32(C.int(mark))

	// make a gopacket from the raw packet data
	packet := gopacket.NewPacket(buffer, layers.LayerTypeIPv4, gopacket.DecodeOptions{Lazy: true, NoCopy: true})

	// get the  IPv4 layer
	ipLayer := packet.Layer(layers.LayerTypeIPv4)
	if ipLayer == nil {
		return (pmark)
	}
	ip := ipLayer.(*layers.IPv4)

	var srcport, dstport uint16

	// get the TCP layer
	tcpLayer := packet.Layer(layers.LayerTypeTCP)
	if tcpLayer != nil {
		tcp := tcpLayer.(*layers.TCP)
		srcport = uint16(tcp.SrcPort)
		dstport = uint16(tcp.DstPort)
	}

	// get the UDP layer
	udpLayer := packet.Layer(layers.LayerTypeUDP)
	if udpLayer != nil {
		udp := udpLayer.(*layers.UDP)
		srcport = uint16(udp.SrcPort)
		dstport = uint16(udp.DstPort)
	}

	// right now we only care about TCP and UDP
	if (tcpLayer == nil) && (udpLayer == nil) {
		return (pmark)
	}

	var entry support.SessionEntry
	var ok bool

	finder := fmt.Sprintf("%d|%s:%d-%s:%d", uint8(ip.Protocol), ip.SrcIP, srcport, ip.DstIP, dstport)

	/*
	 * If we already have a session entry update the existing, otherwise
	 * create a new entry for the table.
	 */
	if entry, ok = support.FindSessionEntry(finder); ok {
		support.LogMessage("SESSION Found %s in table\n", finder)
		entry.UpdateCount++
	} else {
		support.LogMessage("SESSION Adding %s to table\n", finder)
		entry.SessionId = support.NextSessionId()
		entry.SessionCreation = time.Now()
		entry.UpdateCount = 1
	}

	// TODO - pass the gopacket to the handlers instead of the raw buffer

	// ********** Call all plugin netfilter handler functions here

	c1 := make(chan int32)
	go example.Plugin_netfilter_handler(c1, buffer, length)
	c2 := make(chan int32)
	go classify.Plugin_netfilter_handler(c2, buffer, length)
	c3 := make(chan int32)
	go geoip.Plugin_netfilter_handler(c3, buffer, length, uint(C.uint(conn_id)))

	// ********** End of plugin netfilter callback functions

	// add the mark bits returned from each package handler
	for i := 0; i < 2; i++ {
		select {
		case mark1 := <-c1:
			pmark |= mark1
		case mark2 := <-c2:
			pmark |= mark2
		case mark3 := <-c3:
			pmark |= mark3
		}
	}

	// return the updated mark to be set on the packet
	return (pmark)
}

/*---------------------------------------------------------------------------*/
//export go_conntrack_callback
func go_conntrack_callback(info *C.struct_conntrack_info) {
	var tuple support.Tuple
	var entry support.ConntrackEntry

	var ok bool

	tuple.Protocol = uint8(info.orig_proto)

	tuple.ClientAddr = make(net.IP, 4)
	binary.LittleEndian.PutUint32(tuple.ClientAddr, uint32(info.orig_saddr))
	tuple.ClientPort = uint16(info.orig_sport)

	tuple.ServerAddr = make(net.IP, 4)
	binary.LittleEndian.PutUint32(tuple.ServerAddr, uint32(info.orig_daddr))
	tuple.ServerPort = uint16(info.orig_dport)

	finder := support.Tuple2String(tuple)

	/*
	 * If we already have a conntrack entry update the existing, otherwise
	 * create a new entry for the table.
	 */
	if entry, ok = support.FindConntrackEntry(finder); ok {
		support.LogMessage("CONNTRACK Found %s in table\n", finder)
		entry.UpdateCount++
	} else {
		support.LogMessage("CONNTRACK Adding %s to table\n", finder)
		entry.SessionId = support.NextSessionId()
		entry.SessionCreation = time.Now()
		entry.SessionTuple = tuple
		entry.UpdateCount = 1
	}

	oldC2sBytes := entry.C2Sbytes
	oldS2cBytes := entry.S2Cbytes
	oldTotalBytes := entry.TotalBytes
	newC2sBytes := uint64(info.orig_bytes)
	newS2cBytes := uint64(info.repl_bytes)
	newTotalBytes := (newC2sBytes + newS2cBytes)
	diffC2sBytes := (newC2sBytes - oldC2sBytes)
	diffS2cBytes := (newS2cBytes - oldS2cBytes)
	diffTotalBytes := (newTotalBytes - oldTotalBytes)

	// In some cases, specifically UDP, a new session takes the place of an old session with the same tuple.
	// In this case the counts go down because its actually a new session. If the total bytes is low, this
	// is probably the case so treat it as a new entry.
	if (diffC2sBytes < 0) || (diffS2cBytes < 0) {
		newC2sBytes = uint64(info.orig_bytes)
		diffC2sBytes = newC2sBytes
		newS2cBytes = uint64(info.repl_bytes)
		diffS2cBytes = newS2cBytes
		newTotalBytes = (newC2sBytes + newS2cBytes)
		diffTotalBytes = newTotalBytes
		return
	}

	c2sRate := float32(diffC2sBytes / 60)
	s2cRate := float32(diffS2cBytes / 60)
	totalRate := float32(diffTotalBytes / 60)

	entry.C2Sbytes = newC2sBytes
	entry.S2Cbytes = newS2cBytes
	entry.TotalBytes = newTotalBytes
	entry.C2Srate = c2sRate
	entry.S2Crate = s2cRate
	entry.TotalRate = totalRate

	entry.SessionActivity = time.Now()

	if info.msg_type == 'D' {
		entry.PurgeFlag = true
	} else {
		entry.PurgeFlag = false
	}

	support.InsertConntrackEntry(finder, entry)

	// ********** Call all plugin conntrack handler functions here

	go example.Plugin_conntrack_handler(int(info.msg_type), &entry)
	go certcache.Plugin_conntrack_handler(int(info.msg_type), &entry)

	// ********** End of plugin netfilter callback functions

}

/*---------------------------------------------------------------------------*/
//export go_netlogger_callback
func go_netlogger_callback(info *C.struct_netlogger_info) {
	var logger support.Logger

	logger.Protocol = uint8(info.protocol)
	logger.IcmpType = uint16(info.icmp_type)
	logger.SrcIntf = uint8(info.src_intf)
	logger.DstIntf = uint8(info.dst_intf)
	logger.SrcAddr = uint32(info.src_addr)
	logger.DstAddr = uint32(info.dst_addr)
	logger.SrcPort = uint16(info.src_port)
	logger.DstPort = uint16(info.dst_port)
	logger.Mark = uint32(info.mark)
	logger.Prefix = C.GoString(info.prefix)

	// ********** Call all plugin netlogger handler functions here

	go example.Plugin_netlogger_handler(&logger)

	// ********** End of plugin netlogger callback functions
}

/*---------------------------------------------------------------------------*/
//export go_child_startup
func go_child_startup() {
	childsync.Add(1)
}

/*---------------------------------------------------------------------------*/
//export go_child_goodbye
func go_child_goodbye() {
	childsync.Done()
}

/*---------------------------------------------------------------------------*/
