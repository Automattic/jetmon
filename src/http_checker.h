
#ifndef __HTTP_CHECKER_H__
#define __HTTP_CHECKER_H__

#include <cstdlib>
#include <string>
#include <string.h>
#include <strings.h>
#include <cerrno>
#include <exception>
#include <iostream>
#include <algorithm>
#include <fcntl.h>
#include <arpa/inet.h>
#include <sys/time.h>
#include <sys/socket.h>
#include <sys/select.h>
#include <unistd.h>

#include <netinet/in_systm.h>
#include <netinet/in.h>
#include <netinet/ip.h>
#include <netinet/ip_icmp.h>
#include <netdb.h>

#define HTTP_DEFAULT_PORT	80
#define MAX_TCP_BUFFER		1024
#define NET_COMMS_TIMEOUT   10

class HTTP_Checker {

public:
	HTTP_Checker();
	~HTTP_Checker();

	void check( std::string p_host_name, int p_port = HTTP_DEFAULT_PORT );
	time_t get_rtt() { return m_triptime; }
	std::string get_str_desc() { return m_str_desc; }
	int get_response_code() { return m_response_code; }

private:
	char m_buf[MAX_TCP_BUFFER];
	int m_sock;
	std::string m_host_name;
	std::string m_str_desc;
	std::string m_host_dir;
	int m_port;
	struct timezone m_tzone;
	time_t m_triptime;
	int m_response_code;

	bool init_socket();
	bool connect();
	bool disconnect();
	std::string send_http_get();
	bool send_bytes( char* p_packet, size_t p_packet_length );
	std::string get_response();

};

#endif	//__HTTP_H__
