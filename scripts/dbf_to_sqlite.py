#!/usr/bin/env python
import os
import sqlite3
import sys
from xml.etree import ElementTree


def generate_columns(table, columns):
	ret = []
	types = {
		"Bool": "bool",
		"Int": "int",
		"Long": "long",
		"ULong": "unsigned long",
		"String": "text",
		"LocString": "text",
		"AssetPath": "text",
	}
	for name, type in columns:
		cn = name.lower()
		if type == "LocString":
			cn += "_enus"
		t = "%s %s" % (cn, types[type])
		if cn == "id":
			t += " primary key"
		ret.append(t)

	return ",\n".join(ret)


def get_field(table, record, column, type):
	xpath = './Field[@column="%s"]' % (column)
	if type == "LocString":
		xpath += "/enUS"

	data = record.find(xpath)
	if data is None:
		return None
	data = data.text

	if type == "Bool":
		return True if data == "True" else False
	elif type in ("Int", "Long", "ULong"):
		if data is None:
			return None
		return int(data)
	return data


def main():
	if len(sys.argv) < 3:
		sys.stderr.write("USAGE: %s [datadir] [dbfile]\n" % (sys.argv[0]))
		exit(1)
	datadir = sys.argv[1]
	dbfile = sys.argv[2]

	connection = sqlite3.connect(dbfile)

	files = [
		"ACHIEVE.xml",
		"ADVENTURE.xml",
		"ADVENTURE_DATA.xml",
		"ADVENTURE_MISSION.xml",
		"BANNER.xml",
		"BOARD.xml",
		"BOOSTER.xml",
		"CARD_BACK.xml",
		"CARD.xml",
		"FIXED_REWARD.xml",
		"FIXED_REWARD_ACTION.xml",
		"FIXED_REWARD_MAP.xml",
		"HERO.xml",
		"SCENARIO.xml",
		"SEASON.xml",
		"WING.xml",
	]

	for path in files:
		tablename = os.path.splitext(path)[0].lower()
		with open(os.path.join(datadir, "DBF", path), "r", encoding="utf-8") as f:
			xml = ElementTree.parse(f)

			cols = [(e.attrib["name"], e.attrib["type"]) for e in xml.findall("Column")]

			_columns = generate_columns(tablename, cols)
			create_tbl = "CREATE TABLE IF NOT EXISTS dbf_%s (%s)" % (tablename, _columns)
			connection.execute(create_tbl)

			values = []
			for record in xml.findall("Record"):
				fields = [get_field(tablename, record, column, type) for column, type in cols]

				values.append(fields)

			values_ph = ", ".join("?" for c in cols)
			insert_into = "INSERT INTO dbf_%s VALUES (%s)" % (tablename, values_ph)

			print(insert_into)

			connection.executemany(insert_into, values)

	# Add card names
	connection.execute("ALTER TABLE dbf_card ADD COLUMN name_enus text")

	# Add card class
	connection.execute("ALTER TABLE dbf_card ADD COLUMN class_id int")

	cur = connection.cursor()
	cur.execute("SELECT id, note_mini_guid FROM dbf_card")
	rows = cur.fetchall()

	with open(os.path.join(datadir, "CardDefs.xml"), "r", encoding="utf-8") as f:
		xml = ElementTree.parse(f)

		for pk, id in rows:
			xpath = 'Entity[@CardID="%s"]' % (id)
			e = xml.find(xpath)
			if e is None:
				print("WARNING: Could not find card %r in hs-data." % (id))
				continue
			name = e.find('Tag[@enumID="185"]/enUS').text
			connection.execute("UPDATE dbf_card SET name_enus = ? WHERE id = ?", (name, pk))
			card_class_elem = e.find('Tag[@enumID="199"]')
			card_class = 0
			if card_class_elem is not None:
				card_class = int(card_class_elem.attrib["value"])
			connection.execute("UPDATE dbf_card SET class_id = ? WHERE id = ?", (card_class, pk))

	connection.commit()
	connection.close()


if __name__ == "__main__":
	main()
