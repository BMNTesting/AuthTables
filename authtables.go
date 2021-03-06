package main

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"gopkg.in/redis.v4"
	"io/ioutil"
	"net/http"
	"regexp"
	"time"
)

//Main
func main() {

	//First time online, load historical data for bloom
	loadRecords()

	//Configure log Loglevel

	//Announce that we're running
	log.Info("AuthTables is running.")
	//Add routes, then open a webserver
	http.HandleFunc("/add", addRequest)
	http.HandleFunc("/check", checkRequest)
	http.HandleFunc("/reset", resetRequest)
	log.Error(http.ListenAndServe(":8080", nil))

}

func getRecordHashesFromRecord(rec Record) (recordhashes RecordHashes) {

	rh := RecordHashes{
		uid:    []byte(rec.Uid),
		uidMID: []byte(fmt.Sprintf("%s:%s", rec.Uid, rec.Mid)),
		uidIP:  []byte(fmt.Sprintf("%s:%s", rec.Uid, rec.Ip)),
		uidALL: []byte(fmt.Sprintf("%s:%s:%s", rec.Uid, rec.Ip, rec.Mid)),
		ipMID:  []byte(fmt.Sprintf("%s:%s", rec.Ip, rec.Mid)),
		midIP:  []byte(fmt.Sprintf("%s:%s", rec.Mid, rec.Ip)),
	}

	return rh
}

func check(rec Record) (b bool) {
	//We've received a request to /check and now
	//we need to see if it's suspicious or not.

	//Create []byte Strings for bloom
	rh := getRecordHashesFromRecord(rec)

	//These is ip:mid and mid:ip, useful for `key`
	//commands hunting for other bad guys. This May
	//be a separate db, sharded elsewhere in the future.
	//Example: `key 1.1.1.1:*` will reveal new machine ID's
	//seen on this host.
	//This may include evil data, which is why we don't attach to a user.
	writeRecord(rh.ipMID)
	writeRecord(rh.midIP)

	//Do we have it in bloom?
	//if filter.Test([]byte(r.URL.Path[1:])) {
	if filter.Test(rh.uidALL) {
		//We've seen everything about this user before. MachineID, IP, and user.
		log.WithFields(log.Fields{
			"uid": rec.Uid,
			"mid": rec.Mid,
			"ip":  rec.Ip,
		}).Debug("Known user information.")

		//Write Everything.
		//defer writeUserRecord(rh)
		return true
	} else if (filter.Test(rh.uidMID)) || (filter.Test(rh.uidIP)) {

		log.WithFields(log.Fields{
			"uid": rec.Uid,
			"mid": rec.Mid,
			"ip":  rec.Ip,
		}).Debug("Authentication is partially within graph. Expanding graph.")
		defer writeUserRecord(rh)
		return true

	} else if !(filter.Test(rh.uid)) {

		log.WithFields(log.Fields{
			"uid": rec.Uid,
			"mid": rec.Mid,
			"ip":  rec.Ip,
		}).Debug("New user. Creating graph")

		defer writeUserRecord(rh)
		return true

	} else {

		log.WithFields(log.Fields{
			"uid": rec.Uid,
			"mid": rec.Mid,
			"ip":  rec.Ip,
		}).Info("Suspicious authentication.")
		return false
	}

}

func isStringSane(s string) (b bool) {

	matched, err := regexp.MatchString("^[A-Za-z0-9.]{0,60}$", s)
	if err != nil {
		fmt.Println(err)
	}

	return matched
}

func isRecordSane(r Record) (b bool) {

	return (isStringSane(r.Mid) && isStringSane(r.Ip) && isStringSane(r.Uid))

}
func sanitizeError() {
	log.Warn("Bad data received. Sanitize fields in application before sending to remove this message.")
}

func requestToJSON(r *http.Request) (m Record) {
	//Get our body from the request (which should be JSON)
	err := r.ParseForm()
	if err != nil {
		fmt.Println("error:", err)
		log.Warn("Trouble parsing the form from the request")
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		fmt.Println("error:", err)
		log.Warn("Trouble reading JSON from request")
	}

	//Cast our JSON body content to prepare for Unmarshal
	clientAuthdata := []byte(body)

	//Decode some JSON and get it into our Record struct
	var rec Record
	err = json.Unmarshal(clientAuthdata, &rec)
	if err != nil {
		log.Warn("Trouble with Unmarhal of JSON received from client.")
	}

	return rec
}

//Main routing handlers
func addRequest(w http.ResponseWriter, r *http.Request) {
	var m Record
	m = requestToJSON(r)

	if isRecordSane(m) {
		log.WithFields(log.Fields{
			"uid": m.Uid,
			"mid": m.Mid,
			"ip":  m.Ip,
		}).Debug("Adding user.")

		if add(m) {
			fmt.Fprint(w, "ADD")
		} else {
			fmt.Fprint(w, "ADD")
			log.Error("Something went wrong adding user.")
		} //Currently we fail open.
	} else {
		sanitizeError()
	}

}

func add(rec Record) (b bool) {

	//JSON record is sent to /add, we add all of it to bloom.
	rh := getRecordHashesFromRecord(rec)
	defer writeUserRecord(rh)
	return true
}

func resetRequest(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "RESET")
	defer loadRecords()
}

func checkRequest(w http.ResponseWriter, r *http.Request) {
	var m Record
	m = requestToJSON(r)

	//Only let sane data through the gate.
	if isRecordSane(m) {

		if check(m) {
			fmt.Fprint(w, "OK")
		} else {
			fmt.Fprint(w, "BAD")
		}
	} else {
		//We hit this if nasty JSON data came through. Shouldn't touch bloom or redis.
		//To remove this message, don't let your application send UID, IP, or MID that doesn't match "^[A-Za-z0-9.]{0,60}$"
		sanitizeError()
		fmt.Fprintln(w, "BAD")
	}
}

func writeRecord(key []byte) {

	err := client.Set(string(key), 1, 0).Err()
	if err != nil {
		//(TODO Try to make new connection)
		rebuildConnection()

		log.WithFields(log.Fields{
			"error": err,
		}).Error("Problem connecting to database.")

	}

}

func rebuildConnection() {
	log.Debug("Attempting to reconnect...")
	client = redis.NewClient(&redis.Options{
		Addr:     c.Host + ":" + c.Port,
		Password: c.Password, // no password set
		DB:       0,          // use default DB
	})
}

func loadRecords() {
	timeTrack(time.Now(), "Loading records")

	var cursor uint64
	var n int

	//Empty our filter before re-filling
	filter.ClearAll()
	for {
		var keys []string
		var err error
		keys, cursor, err = client.Scan(cursor, "", 10).Result()
		if err != nil {
			log.Error("Could not connect to database. Continuing without records")
			break
		}
		n += len(keys)

		for _, element := range keys {
			filter.Add([]byte(element))
		}

		if cursor == 0 {
			break
		}
	}
	log.WithFields(log.Fields{
		"number": n,
	}).Debug("Loaded historical records.")
}

func canGetKey(s string) bool {
	err := client.Get(s).Err()
	if err != nil {
		return false
	}
	return true
}

func writeUserRecord(rh RecordHashes) {

	err := client.MSet(string(rh.uid), 1, string(rh.uidMID), 1, string(rh.uidIP), 1, string(rh.uidALL), 1).Err()
	if err != nil {
		log.Error("MSet failed.")
		log.Error(err)
	}

	//Bloom
	filter.Add(rh.uidMID)
	filter.Add(rh.uidIP)
	filter.Add(rh.uid)
	filter.Add(rh.uidALL)
}

func timeTrack(start time.Time, name string) {
	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"time":  elapsed.String(),
		"event": name,
	}).Debug("Time tracked")
}

//Only using init to configure logging. See configuration.go
func init() {
	level, err := log.ParseLevel(c.Loglevel)
	if err != nil {
		log.Error("Issue setting log level. Make sure log level is a string: debug, warn, info, error, panic")
	}
	log.SetLevel(level)
}
