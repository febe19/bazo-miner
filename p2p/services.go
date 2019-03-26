package p2p

import (
	"github.com/bazo-blockchain/bazo-miner/storage"
	"time"
)


var (
	sendingMap  map[string]*delayedMessagesPerSender
)

type delayedMessagesPerSender struct {
	peer *peer
	delayedMessages [][]byte
}

//This is not accessed concurrently, one single goroutine. However, the "peers" are accessed concurrently, therefore the
//Thread-safe implementation.
func peerService() {
	for {
		select {
		case p := <-register:
			peers.add(p)
		case p := <-disconnect:
			peers.closeChannelMutex.Lock()
			peers.delete(p)
			close(p.ch)
			peers.closeChannelMutex.Unlock()
		}
	}
}

func minerBroadcastService() {
	sendingMap = map[string]*delayedMessagesPerSender{}

	for {
		select {
		case msg := <-minerBrdcstMsg:
			if len(minerBrdcstMsg) > 0 {
				logger.Printf("Inside MinerBrdCst: len(minerBrdcstMsg) = %v", len(minerBrdcstMsg))
			}
			go sendAndSearchMessages(msg)
		}
	}
}

func clientBroadcastService() {

	for {
		select {
		case msg := <-clientBrdcstMsg:
			for p := range peers.clientConns {
				if peers.contains(p.getIPPort(),PEERTYPE_CLIENT) {
					p.ch <- msg
				} else {
					logger.Printf("CHANNEL_CLIENT: Wanted to send to %v, but %v is not in the peers.minerConns anymore", p.getIPPort(), p.getIPPort())
				}
			}
		}
	}
}

//This function does send the current and possible previous not send messages
func sendAndSearchMessages(msg []byte) {
	for _, p := range sendingMap {
		//Check if there is a valid connection to peer p, if not, store message
		//if peers.minerConns[p.peer] {
		if peers.contains(p.peer.getIPPort(), PEERTYPE_MINER) {

			//If connection is valid, send message.
			//This is used to get the newest channel for given IP+Port. In case of an update in the background
			peers.closeChannelMutex.Lock()

			//Update Peer
			_, _ = isConnectionAlreadyInSendingMap(p.peer, sendingMap)
			receiver := sendingMap[p.peer.getIPPort()].peer

			//Check peer is still in the minerConns
			if peers.contains(receiver.getIPPort(), PEERTYPE_MINER) {
				receiver.ch <- msg
			}

			//Send previously stored messages for this miner as well.
			for _, hMsg := range p.delayedMessages {
				//Send historic not yet sent transaction and remove it.

				//If the receiver channel is full, continue such that the program is not blocked...
				if len(receiver.ch) == 1000 {
					continue
				}
				if peers.contains(receiver.getIPPort(), PEERTYPE_MINER) {
					receiver.ch <- hMsg
				}

				//Remove Sent message
				p.delayedMessages = p.delayedMessages[1:]
			}
			peers.closeChannelMutex.Unlock()
		} else {
			//Store messages which are not sent du to connectivity issues.
			messages := p.delayedMessages
			//Check that not too many delayed messages are stored.
			if len(messages) > 40 {
				messages = messages[1:]
			}

			//Store message for this specific miner connection.
			p.delayedMessages = append(messages, msg)
		}
	}
}

//This function checks if a connection was already established once and if the peer "behind" the IP + Port changed.
// This can happen all time when new connecting, because e.g a new channel (p.ch) is set up once adding a new peer
// (even if it was added before). If the peer changes as well, it gets updated in the sendingMap.
func isConnectionAlreadyInSendingMap(p *peer, sendingMap map[string]*delayedMessagesPerSender) (alreadyInSenderMap bool, needsUpdate bool) {

	for _, connection := range sendingMap {
		if connection.peer.getIPPort() == p.getIPPort() {
			if connection.peer != p {
				sendingMap[p.getIPPort()] = &delayedMessagesPerSender{p, connection.delayedMessages}
				return true, true
			} else {
				return true, false
			}
		}
	}
	return false, false
}

//Belongs to the broadcast service.
func peerBroadcast(p *peer) {
	logger.Printf("CreatedPeerbroadcast for %v", p.getIPPort())

	for msg := range p.ch {
		go sendData(p, msg)
	}
}

//Single goroutine that makes sure the system is well connected.
func checkHealthService() {
	for {
		//time.Sleep(HEALTH_CHECK_INTERVAL * time.Second)  Between 5 and 30 seconds check interval.
		var nrOfMiners = 1
		knownConnections := peers.getAllPeers(PEERTYPE_MINER)
		if len(knownConnections) > 1 {
			nrOfMiners = len(knownConnections)
		}
		if len(knownConnections) > 6 {
			nrOfMiners = 6
		}

		time.Sleep(time.Duration(nrOfMiners) * 5 * time.Second)  //Dynamic searching for neighbours interval --> 5 times the number of miners

		if Ipport != storage.Bootstrap_Server && !peers.contains(storage.Bootstrap_Server, PEERTYPE_MINER) {
			p, err := initiateNewMinerConnection(storage.Bootstrap_Server)
			if p == nil || err != nil {
				selfConnect := "Cannot self-connect"
				if err.Error()[0:9] != selfConnect[0:9] {
					logger.Printf("Initiating new miner connection failed: %v", err)
				}
			} else {
				go peerConn(p)
			}
		}

		//Periodically check if we are well-connected
		if peers.len(PEERTYPE_MINER) >= MIN_MINERS {
			continue
		}

		//The only goto in the code (I promise), but best solution here IMHO.
	RETRY:
		select {
		//iplistChan gets filled with every incoming neighborRes, they're consumed here.
		case ipaddr := <-iplistChan:
			if !peerExists(ipaddr) && !peerSelfConn(ipaddr) {

				p, err := initiateNewMinerConnection(ipaddr)
				if err != nil {
					logger.Printf("Initiating new miner connection failed: %v", err)
				}
				if p == nil || err != nil {
					goto RETRY
				}
				go peerConn(p)
				break
			}
		default:
			//In case we don't have any ip addresses in the channel left, make a request to the network.
			PrintMinerCons()
			neighborReq()
			logger.Printf("    |-- Request Neighbors...        |\n                                                      |_______________________________|")
			break
		}
	}
}

//Calculates periodically system time from available sources and broadcasts the time to all connected peers.
func timeService() {
	//Initialize system time.
	systemTime = time.Now().Unix()
	go func() {
		for {
			time.Sleep(UPDATE_SYS_TIME * time.Second)
			writeSystemTime()
		}
	}()

	for {
		time.Sleep(TIME_BRDCST_INTERVAL * time.Second)
		packet := BuildPacket(TIME_BRDCST, getTime())
		minerBrdcstMsg <- packet
	}
}
