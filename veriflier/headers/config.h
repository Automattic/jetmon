
#ifndef __CONFIG_H__
#define __CONFIG_H__

#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonValue>
#include <QFile>

#include <iostream>

class Config {
public:
	static Config* instance() { return m_instance; }

	int get_int_value( QString name );
	bool get_bool_value( QString name );
	QString get_string_value( QString name );

private:
	Config();
	static Config *m_instance;
	QJsonDocument m_json;

	void load_config_file();
};

#endif // __CONFIG_H__

