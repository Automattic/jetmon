
#ifndef __CHECKCONTROLLER_H__
#define __CHECKCONTROLLER_H__

#include <QObject>
#include <QVector>
#include <QMutex>
#include <QDateTime>
#include <QTimer>
#include <QSslSocket>
#include <QSslConfiguration>

#include "headers/check_thread.h"
#include "headers/jetmon_server.h"

#define NOT_ASSIGNED -1
#define PRE_ASSIGNED -2

struct Runner {
	CheckThread* ct;
	int checking;
};

class CheckController : public QObject
{
	Q_OBJECT
public:
	explicit CheckController( const QSslConfiguration *m_ssl_config,
							const int jetmon_server_port,
							const int max_runners = 20,
							const int max_checks = 50,
							const QString &veriflier_name = "",
							const QString &auth_token = "",
							const int net_timeout = 20000,
							const bool debug = false );

	~CheckController();
	void addCheck( HealthCheck* hc );
	void addChecks( QVector<HealthCheck*> hcs );

public slots:
	void startChecking( HealthCheck* hc );
	void finishedChecking( int thread_index, qint64 blog_id, QString monitor_url, int status, int http_code, int rtt );
	void finishedSending( JetmonServer* js, int status, int rtt );
	void ticked();

signals:
	void startCheck( HealthCheck* hc );

private:
	QVector<HealthCheck*> m_checks;
	QVector<Runner*> m_runners;

	QMap<QString, QJsonDocument> m_check_results;

	const QSslConfiguration *m_ssl_config;
	int m_jetmon_server_port;
	QSslSocket *m_socket;
	QMutex m_check_lock;
	QTimer *m_ticker;

	int m_max_checkers;
	int m_max_checks;
	int m_checking;
	int m_checked;

	QString m_veriflier_name;
	QString m_auth_token;
	int m_net_timeout;
	bool m_debug;

	inline bool haveCheck( qint64 blog_id, QString monitor_url );
	int selectRunner();
	void sendResults();
	QString post_http_header( QString jetmon_server, int content_size );
	void sendToJetmonServer( QString jetmon_server, QByteArray status_data );
	QJsonDocument parse_json_response( QByteArray &raw_data );
	int readResponse();
};

#endif // __CHECKCONTROLLER_H__
