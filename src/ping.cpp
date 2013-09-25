

#include "ping.h"

using namespace std;

Pinger::Pinger() : m_received( 0 ), m_transmitted( 0 ), m_min( 999999999 ), m_max( 0 ), m_sum( 0 ) {
	m_ident = getpid() & 0xFFFF;
}

bool Pinger::ping( string hostname, int p_num_packets, const int& p_min_packets ) {
	struct sockaddr_in *to = (struct sockaddr_in *) &m_dest;
	struct protoent *proto;
	struct hostent *hp;
	u_char m_packet[PACKET_BUFFER];

	bzero( (char *)&m_dest, sizeof( struct sockaddr ) );
	to->sin_family = AF_INET;
	to->sin_addr.s_addr = ::inet_addr( hostname.c_str() );

	if( 0 == to->sin_addr.s_addr ) {
		hp = ::gethostbyname( hostname.c_str() );
		if ( hp ) {
			to->sin_family = hp->h_addrtype;
			bcopy( hp->h_addr, (caddr_t)&to->sin_addr, hp->h_length );
			hostname = hp->h_name;
		} else {
			cerr << "unknown host:" << hostname.c_str() << endl;
			return false;
		}
	}

	proto = ::getprotobyname( "icmp" );

	if ( NULL == proto ) {
		cerr << "icmp: unknown protocol" << endl;
		return false;
	}

	m_sock = ::socket( AF_INET, SOCK_RAW, proto->p_proto );

	if ( m_sock < 0 ) {
		cerr << "error creating ping socket" << endl;
		return false;
	}

	struct sockaddr from;
	size_t send_len = sizeof( m_packet );
	socklen_t fromlen = sizeof( from );
	size_t rec_size;
	struct timeval timeout;
	fd_set read_fds;
	time_t time_end;

	while ( p_num_packets--  ) {

		time_end = time(NULL);
		time_end += PING_TIMEOUT_SEC;

		if ( ! send_ping() )
			continue;

		do
		{
			timeout.tv_sec = 0;
			timeout.tv_usec = 50000;

			FD_ZERO( &read_fds );
			FD_SET( m_sock, &read_fds );

			::select( m_sock + 1, &read_fds, NULL, NULL, &timeout );
		}while( (FD_ISSET( m_sock, &read_fds) == 0) && ( time_end > time(NULL) ) );

		if ( FD_ISSET( m_sock, &read_fds ) )
		{
			if ( (rec_size = ::recvfrom( m_sock, (char *)m_packet, send_len, 0, &from, &fromlen ) ) == 0 ) {
				if( errno == EINTR )
					continue;

				if ( rec_size != send_len ) {
					cerr << "ping: recvfrom error" << endl;
					continue;
				}
			}

			read_packet( (char *)m_packet, rec_size, (struct sockaddr_in *)&from );
		}
	}
    
    ::close( m_sock );
    m_sock = -1;
    
	return ( m_received >= p_min_packets );
}

size_t Pinger::get_avg_usec() {
	return ( m_sum / m_transmitted );
}

bool Pinger::send_ping()
{
	static u_char outpack[PACKET_BUFFER];
	struct icmp *icp = (struct icmp *) outpack;
	size_t i_ret, cc = PACKET_SIZE + 8;
	struct timeval *tp = (struct timeval *) &outpack[8];
	u_char *datap = &outpack[ 8 + sizeof(struct timeval) ];

	icp->icmp_type = ICMP_ECHO;
	icp->icmp_code = 0;
	icp->icmp_cksum = 0;
	icp->icmp_seq = m_transmitted++;
	icp->icmp_id = m_ident;

	gettimeofday( tp, &m_tzone );

	for( int i = 8; i < PACKET_SIZE; i++)
		*datap++ = i;

	icp->icmp_cksum = calc_checksum( (u_short *)icp, PACKET_SIZE + 8 );
	i_ret = ::sendto( m_sock, outpack, cc, 0, &m_dest, sizeof(struct sockaddr) );

	if( i_ret != cc )  {
		cerr << "Error sending ping";
		return false;
	} else
		return true;
}

void Pinger::read_packet( char *buf, size_t cc, struct sockaddr_in *from ) {
	struct ip *ip;
	struct icmp *icp;
	struct timeval tv;
	struct timeval *tp;
	size_t hlen, triptime;

	from->sin_addr.s_addr = ntohl( from->sin_addr.s_addr );
	gettimeofday( &tv, &m_tzone );

	ip = (struct ip *) buf;
	hlen = ip->ip_hl << 2;
	if (cc < hlen + ICMP_MINLEN) {
		return;
	}
	cc -= hlen;
	icp = (struct icmp *)(buf + hlen);
	if( icp->icmp_id != m_ident )
		return;

	tp = (struct timeval *)&icp->icmp_data[0];
	tv_subtract( &tv, tp );
	triptime = tv.tv_sec * 1000000 + ( tv.tv_usec );
	m_sum += triptime;
	if( triptime < m_min )
		m_min = triptime;
	if( triptime > m_max )
		m_max = triptime;

	m_received++;
}

u_short Pinger::calc_checksum( u_short *addr, int len ) {
	int nleft = len;
	u_short *w = addr;
	u_short answer;
	int sum = 0;

	while( nleft > 1 ) {
		sum += *w++;
		nleft -= 2;
	}

	if( nleft == 1 ) {
		u_short	u = 0;
		*(u_char *)(&u) = *(u_char *)w ;
		sum += u;
	}

	sum = (sum >> 16) + (sum & 0xffff);
	sum += (sum >> 16);
	answer = ~sum;
	return answer;
}

void Pinger::tv_subtract( struct timeval *out, struct timeval *in ) {
	if ( (out->tv_usec -= in->tv_usec) < 0 )   {
		out->tv_sec--;
		out->tv_usec += 1000000;
	}
	out->tv_sec -= in->tv_sec;
}


