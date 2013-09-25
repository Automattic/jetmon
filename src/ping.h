
#ifndef __PINGER_H__
#define __PINGER_H__

#include <cstdio>
#include <string>
#include <strings.h>
#include <iostream>
#include <cerrno>

#include <sys/time.h>
#include <sys/param.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <sys/file.h>

#include <arpa/inet.h>
#include <sys/select.h>
#include <unistd.h>

#include <netinet/in_systm.h>
#include <netinet/in.h>
#include <netinet/ip.h>
#include <netinet/ip_icmp.h>
#include <netdb.h>

#define	PING_TIMEOUT_SEC	1
#define	PACKET_SIZE			54
#define PACKET_BUFFER		127

class Pinger {

public:
	Pinger();

	bool ping( std::string hostname, int p_num_packets = 3, const int& p_min_packets = 2 );
	size_t get_avg_usec();

private:
	int m_sock;
	int m_ident;
	struct sockaddr m_dest;
	struct timezone m_tzone;

	int m_received;
	int m_transmitted;

	size_t m_min;
	size_t m_max;
	size_t m_sum;

	bool send_ping();
	u_short calc_checksum( u_short *addr, int len );
	void tv_subtract( struct timeval *out, struct timeval *in );
	void read_packet(char *buf, size_t cc, struct sockaddr_in *from );

};

#endif	//__PINGER_H__

