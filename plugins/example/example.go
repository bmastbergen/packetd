package example

import (
	"encoding/hex"
	"github.com/untangle/packetd/services/dispatch"
	"github.com/untangle/packetd/services/logger"
)

// PluginStartup function is called to allow plugin specific initialization. We
// increment the argumented WaitGroup so the main process can wait for
// our shutdown function to return during shutdown.
func PluginStartup() {
	logger.LogInfo("PluginStartup(%s) has been called\n")
	dispatch.InsertNfqueueSubscription("example", 2, PluginNfqueueHandler)
	dispatch.InsertConntrackSubscription("example", 2, PluginConntrackHandler)
	dispatch.InsertNetloggerSubscription("example", 2, PluginNetloggerHandler)
}

// PluginShutdown function called when the daemon is shutting down. We call Done
// for the argumented WaitGroup to let the main process know we're finished.
func PluginShutdown() {
	logger.LogInfo("PluginShutdown(%s) has been called\n")
}

// PluginNfqueueHandler receives a NfqueueMessage which includes a Tuple and
// a gopacket.Packet, along with the IP and TCP or UDP layer already extracted.
// We do whatever we like with the data, and when finished, we return an
// integer via the argumented channel with any bits set that we want added to
// the packet mark.
func PluginNfqueueHandler(mess dispatch.NfqueueMessage, ctid uint32, newSession bool) dispatch.NfqueueResult {
	// our example simply dumps the raw message to the console
	logger.LogDebug("NfqueueHandler received %d BYTES from %s to %s\n%s\n", mess.Length, mess.IPlayer.SrcIP, mess.IPlayer.DstIP, hex.Dump(mess.Packet.Data()))

	var result dispatch.NfqueueResult
	result.Owner = "example"
	result.SessionRelease = true
	result.PacketMark = 0

	return result
}

// PluginConntrackHandler receives conntrack dispatch. The message will be one
// of three possible values: N, U, or D for new entry, an update to an existing
// entry, or delete of an existing entry.
func PluginConntrackHandler(message int, entry *dispatch.ConntrackEntry) {
	logger.LogDebug("ConntrackHandler MSG:%c ID:%d PROTO:%d SADDR:%s SPORT:%d DADDR:%s DPORT:%d TX:%d RX:%d UC:%d\n",
		message,
		entry.ConntrackID,
		entry.ClientSideTuple.Protocol,
		entry.ClientSideTuple.ClientAddress,
		entry.ClientSideTuple.ClientPort,
		entry.ClientSideTuple.ServerAddress,
		entry.ClientSideTuple.ServerPort,
		entry.C2Sbytes,
		entry.S2Cbytes,
		entry.EventCount)
}

// PluginNetloggerHandler receives NFLOG dispatch.
func PluginNetloggerHandler(netlogger *dispatch.NetloggerMessage) {
	logger.LogDebug("NetloggerHandler PROTO:%d ICMP:%d SIF:%d DIF:%d SADR:%s DADR:%s SPORT:%d DPORT:%d MARK:%X PREFIX:%s\n",
		netlogger.Protocol,
		netlogger.IcmpType,
		netlogger.SrcInterface,
		netlogger.DstInterface,
		netlogger.SrcAddress,
		netlogger.DstAddress,
		netlogger.SrcPort,
		netlogger.DstPort,
		netlogger.Mark,
		netlogger.Prefix)
}
