package mongotest

import (
	"context"
	"fmt"

	"github.com/tophergopher/easymongo"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

func ExampleEasyMongoWithContainer() {
	type fooRecord struct {
		Name string `bson:"name"`
	}
	// Spawn a new container with a unique connection
	err := EasyMongoWithContainer(func(c *easymongo.Connection) error {
		// Connect to the DB and insert a test record
		coll := c.D("exampleDB").C("exampleCollection")
		record := fooRecord{
			Name: "Topher!",
		}
		_, err := coll.Insert().One(&record)
		if err != nil {
			return err
		}
		// Try to look-up that record
		lookupRecord := fooRecord{}
		err = coll.Find(primitive.M{"name": record.Name}).One(&lookupRecord)
		if err != nil {
			return err
		}
		// Print out the name of that record
		fmt.Println(lookupRecord.Name)
		return nil
	})
	if err != nil {
		fmt.Println("Issue running queries:")
		fmt.Println(err)
	}
	// Output: Topher!
}

func ExampleMongoClientWithContainer() {
	type fooRecord struct {
		Name string `bson:"name"`
	}
	// Spawn a new container with a unique connection
	err := MongoClientWithContainer(func(c *mongo.Client) error {
		// Connect to the DB and insert a test record
		coll := c.Database("exampleDB").Collection("exampleCollection")
		record := fooRecord{
			Name: "Topher!",
		}
		_, err := coll.InsertOne(context.Background(), &record)
		if err != nil {
			return err
		}
		// Try to look-up that record
		lookupRecord := fooRecord{}
		err = coll.FindOne(context.Background(), primitive.M{"name": record.Name}).Decode(
			&lookupRecord)
		if err != nil {
			return err
		}
		// Print out the name of that record
		fmt.Println(lookupRecord.Name)
		return nil
	})
	if err != nil {
		fmt.Println("Issue running queries:")
		fmt.Println(err)
	}
	// Output: Topher!
}

// func ExampleExportToJSON() {
// 	// Spawn a new container with a unique connection
// 	type fooRecord struct {
// 		Name string `bson:"name"`
// 	}
// 	testConn, err := NewTestConnection(true)
// 	defer testConn.KillMongoContainer()

// 	// Connect to the collection
// 	coll := testConn.Database("exampleDB").Collection("exampleCollection")
// 	// And insert some saple data
// 	record := fooRecord{
// 		Name: "Topher!",
// 	}
// 	_, err = coll.Insert().ManyFromInterfaceSlice([]interface{}{
// 		record, record, record, record, record, record, record, record,
// 	})
// 	if err != nil {
// 		fmt.Println(err)
// 		return
// 	}
// 	fpath, err := testConn.DatabaseExporter("exampleDB", "exampleCollection").Filepath(
// 		"export_test.json").ToJSONFile(true, true, false, false)
// 	if err != nil {
// 		fmt.Println(err)
// 		return
// 	}
// 	defer os.Remove(fpath)
// 	bytes, err := ioutil.ReadFile(fpath)
// 	if err != nil {
// 		fmt.Println(err)
// 		return
// 	}
// 	fmt.Println(string(bytes))
// 	fmt.Println(fpath)
// 	// Output: export_test.json
// }
