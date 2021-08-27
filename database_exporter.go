package mongotest

import (
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/mongodb/mongo-tools/common/log"
	"github.com/mongodb/mongo-tools/common/util"
	"github.com/mongodb/mongo-tools/mongoexport"
)

// Existing entrypoint - NewTestConnection(spinupDockerContainer bool) (*TestConnection, error)
// This entrypoint has disadvantages as it:
// - locks you into using easymongo (not a huge deal)
// - doesn't allow for TLS connectivity (but we could add a bool here)
// - doesn't allow a TLS data to be passed back up

// Let's design some new entrypoints
// TODO: Are there better names for these entrypoint functions?
// We want a normal entrypoint for those that don't care
//   - NewTestConnection(spinupDockerContainer bool) (*TestConnection, error)
// We want an entrypoint that allows a function to be run and the container and connection reaps itself
//   - EasyMongoWithContainer(f func(c *easymongo.Connection) error) (err error)
// And probably a similar one for people that don't want to adopt easymongo
//   - MongoClientWithContainer(f func(m *mongo.Client) error) error
// We want an entrypoint that configures mongo with TLS and returns the necessary configuration
//   -

// We need the ability to provide an optional port, TLS on/off, image version
// We need to get back the mongoURI, containerID
// If TLS is on, we also need some additional metadata (pem file, tmpFile handle) in order
// to finish configuring the DB connection.

// DatabaseExporter is a wrapper to enable easily exporting a live database
// TODO: Move this into easymongo
type DatabaseExporter struct {
	baseOpts     mongoexport.Options
	compressToGZ bool
	filepath     string
	// exporter     *mongoexport.MongoExport
	testConn *TestConnection
}

// DatabaseExporter returns an object which can be used to export a DB
// TODO: Move to easymongo? Some of these seem very close to Query{}
func (testConn *TestConnection) DatabaseExporter(dbName, collectionName string) *DatabaseExporter {
	rawArgs := []string{"-d", dbName, "-c", collectionName, testConn.mongoURI}
	opts, err := mongoexport.ParseOptions(rawArgs, "mongotest", "master")
	if err != nil {
		panic(fmt.Sprintf("ParseOptions failed: %v", err))
	}

	return &DatabaseExporter{
		baseOpts: opts,
		// exporter: exporter,
		testConn: testConn,
	}
}

func (de *DatabaseExporter) Skip(i int) *DatabaseExporter {
	de.baseOpts.Skip = int64(i)
	return de
}
func (de *DatabaseExporter) Limit(i int) *DatabaseExporter {
	de.baseOpts.Limit = int64(i)
	return de
}
func (de *DatabaseExporter) Query(q string) *DatabaseExporter {
	de.baseOpts.Query = q
	return de
}
func (de *DatabaseExporter) Sort(s string) *DatabaseExporter {
	de.baseOpts.Sort = s
	return de
}
func (de *DatabaseExporter) Filepath(f string) *DatabaseExporter {
	de.filepath = f
	return de
}
func (de *DatabaseExporter) Database(d string) *DatabaseExporter {
	de.baseOpts.DB = d
	return de
}
func (de *DatabaseExporter) Collection(c string) *DatabaseExporter {
	de.baseOpts.Collection = c
	return de
}

// TODO: UsingStringBuilder
func (de *DatabaseExporter) UsingStringBuilder() {
	// Some sort of byte buffer method here - initialize an io.Writer?
	panic(ErrNotImplemented)
}

// TODO: Bytes
func (de *DatabaseExporter) Bytes() ([]byte, error) {
	return nil, ErrNotImplemented
}

// TODO: String
func (de *DatabaseExporter) String() (string, error) {
	return "", ErrNotImplemented
}

// write performs the write to the file and returns the file
// The caller is expected to close this file.
func (de *DatabaseExporter) write() (*os.File, error) {
	exporter, err := mongoexport.New(de.baseOpts)
	if err != nil {
		log.Logvf(log.Always, "%v", err)
		if se, ok := err.(util.SetupError); ok && se.Message != "" {
			log.Logv(log.Always, se.Message)
		}
		return nil, err
	}
	defer exporter.Close()

	var writer io.Writer
	var file *os.File
	if len(de.filepath) == 0 {
		// For our use-case, spawn a temp file to output to.
		filePattern := fmt.Sprintf("mongoexport-%s-%s-*-%s",
			de.baseOpts.DB, de.baseOpts.Collection, "."+de.baseOpts.Type)
		file, err = ioutil.TempFile(os.TempDir(), filePattern)
	} else {
		file, err = os.Open(de.filepath)
	}
	if err != nil {
		return nil, err
	}

	if de.compressToGZ {
		// TODO: Is this the correct method for doing gzip with this library?
		writer = gzip.NewWriter(file)
	} else {
		writer = file
	}

	// Export everything to the temp file
	numDocs, err := exporter.Export(writer)
	if err != nil {
		log.Logvf(log.Always, "Failed: %v", err)
		// Always remove files in the case of export failure
		file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	log.Logvf(log.Always, "Number of documents written: %d", numDocs)
	return file, err
}

// ToJSONFile writes a JSON formatted file to disk and returns the path the file was written to
func (de *DatabaseExporter) ToJSONFile(prettify, jsonArray, relaxedJSON, compressToGZIP bool) (fpath string, err error) {
	de.baseOpts.Pretty = prettify
	de.baseOpts.JSONFormat = mongoexport.Canonical
	if relaxedJSON {
		de.baseOpts.JSONFormat = mongoexport.Relaxed
	}
	de.baseOpts.Type = "json"
	de.baseOpts.JSONArray = jsonArray
	file, err := de.write()
	if err != nil {
		return "", err
	}
	defer file.Close()
	return file.Name(), nil
}
func (de *DatabaseExporter) CSVFile() (fpath string, err error) {
	de.baseOpts.Type = "csv"
	file, err := de.write()
	if err != nil {
		return "", err
	}
	defer file.Close()
	return file.Name(), nil
}

// // ExportDatabasetoJSON runs mongoexport against the current instance connected to TestConnection
// // for the specified dbName
// // The caller is responsible for closing/deleting the file
// // This code used the main function from mongoexport as a starting point
// // 		https://github.com/mongodb/mongo-tools/blob/f684129d7865/mongoexport/main/mongoexport.go
// // mongoexport/mongoimport correspond to eachother - they are usually for working with JSON
// // the don't fully preserve bson
// func (testConn *TestConnection) ExportDatabasetoJSON(dbName, collectionName string, compressToGZ bool) (mongoExportTempFile *os.File, err error) {

// 	rawArgs := []string{"-d", dbName, "-c", collectionName, testConn.mongoURI}
// 	opts, err := mongoexport.ParseOptions(rawArgs, "mongotest", "master")
// 	if err != nil {
// 		return nil, err
// 	}

// 	exporter, err := mongoexport.New(opts)
// 	if err != nil {
// 		log.Logvf(log.Always, "%v", err)
// 		if se, ok := err.(util.SetupError); ok && se.Message != "" {
// 			log.Logv(log.Always, se.Message)
// 		}

// 		return nil, err
// 	}
// 	defer exporter.Close()

// 	// For our use-case, spawn a temp file to output to.
// 	filePattern := fmt.Sprintf("mongoexport-%s-%s-*-%s", dbName, collectionName, ".json")
// 	mongoExportTempFile, err = ioutil.TempFile(os.TempDir(), filePattern)
// 	if err != nil {
// 		return nil, err
// 	}

// 	var writer io.Writer
// 	if compressToGZ {
// 		writer = gzip.NewWriter(mongoExportTempFile)
// 	} else {
// 		writer = mongoExportTempFile
// 	}

// 	// Export everything to the temp file
// 	numDocs, err := exporter.Export(writer)
// 	if err != nil {
// 		log.Logvf(log.Always, "Failed: %v", err)
// 		_ = mongoExportTempFile.Close()
// 		_ = os.Remove(mongoExportTempFile.Name())
// 		return nil, err
// 	}

// 	if numDocs == 1 {
// 		log.Logvf(log.Always, "exported %v record", numDocs)
// 	} else {
// 		log.Logvf(log.Always, "exported %v records", numDocs)
// 	}
// 	return mongoExportTempFile, err
// }
