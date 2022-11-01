
#ifndef __HTTP_CHECKER_H__
#define __HTTP_CHECKER_H__

#include <cstdlib>
#include <string>
#include <cerrno>
#include <exception>
#include <iostream>
#include <algorithm>
#include <fcntl.h>
#include <arpa/inet.h>
#include <sys/time.h>
#include <sys/socket.h>
#include <sys/select.h>
#include <sys/epoll.h>
#include <sys/types.h>
#include <unistd.h>
#include <chrono>



#include <netinet/tcp.h>
#include <netinet/in_systm.h>
#include <netinet/in.h>
#include <netinet/ip.h>
#include <netinet/ip_icmp.h>
#include <netdb.h>

#include <openssl/crypto.h>
#include <openssl/ssl.h>
#include <openssl/err.h>
#if (SSLEAY_VERSION_NUMBER >= 0x0907000L)
# include <openssl/conf.h>
#endif

#define HTTP_DEFAULT_PORT	80
#define HTTPS_DEFAULT_PORT  443
#define MAX_TCP_BUFFER		1024
#define NET_COMMS_TIMEOUT   20
#define MAX_REDIRECTS       2
#define MAX_EPOLL_EVENTS    10

// Enables the printing of debug messages to stderr
#define DEBUG_MODE          0

// getaddrinfo is much slower than gethostbyname and, although
// it is technically the best way to lookup hosts, only enable
// this on hosts with more than enough CPU compute headroom.
#define USE_GETADDRINFO     0

// Sets whether we compile and use non-blocking socket IO
#define NON_BLOCKING_IO     0

class HTTP_Checker {

public:
	HTTP_Checker();
	~HTTP_Checker();

	void check( std::string p_host_name, int p_port = HTTP_DEFAULT_PORT );
	int get_rtt();
	int get_ttfb() { return m_ttfb; };
	int get_response_code() { return m_response_code; }

private:
	char m_buf[MAX_TCP_BUFFER];
	int m_sock;
	std::string m_host_name;
	std::string m_host_dir;
	int m_port;
	bool m_is_ssl;
	std::chrono::_V2::system_clock::time_point m_tstart;
	time_t m_triptime;
	time_t m_cutofftime;
	int m_response_code;
	std::chrono::_V2::system_clock::time_point m_tstart_ttfb;
	int m_ttfb;

	SSL_CTX *m_ctx;
	SSL *m_ssl;
	BIO *m_sbio;

	bool init_socket( addrinfo *addr );
	bool init_ssl();
	bool connect();
#if USE_GETADDRINFO
	bool connect_getaddrinfo();
#else
	bool connect_gethostbyname();
#endif
	bool disconnect();
#if NON_BLOCKING_IO
	void disconnect_ssl();
#endif
	std::string send_http_get();
	bool send_bytes( char* p_packet, size_t p_packet_length );
	std::string get_response();
	void set_host_response( int redirects );
	bool set_redirect_host_values( std::string p_content );
	void parse_host_values();
};

#endif	//__HTTP_H__

