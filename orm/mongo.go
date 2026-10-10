package orm

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoStore runs the typed builder against MongoDB. Collections are named
// after the table; documents store each column under its db name, and the
// primary key is mirrored into _id for uniqueness and key lookups.
//
//	orm.OpenMongo("mongo", "mongodb://localhost:27017", "app")
//	//orm:table events connection=mongo key_type=uuid
//	err := Events.Query().Where(Events.Kind.Eq("click")).Limit(10).Get(ctx)
type MongoStore struct {
	client *mongo.Client
	db     *mongo.Database
}

// OpenMongo connects and registers a MongoDB connection.
func OpenMongo(name, uri, database string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("orm: connecting to mongodb: %w", err)
	}
	s := &MongoStore{client: client, db: client.Database(database)}
	AddDocStore(name, s)
	return s, nil
}

// Close disconnects the client.
func (s *MongoStore) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

func (s *MongoStore) coll(table string) *mongo.Collection { return s.db.Collection(table) }

// CreateIndex declares an index: db.events.createIndex(...) in a migration.
func (s *MongoStore) CreateIndex(ctx context.Context, table string, keys any, unique bool) error {
	_, err := s.coll(table).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    keys,
		Options: options.Index().SetUnique(unique),
	})
	return err
}

// DropCollection removes a collection (dropIfExists in a migration).
func (s *MongoStore) DropCollection(ctx context.Context, table string) error {
	return s.coll(table).Drop(ctx)
}

func (s *MongoStore) FindDocs(ctx context.Context, table string, p Plan) ([]map[string]any, error) {
	if p.Vector != nil {
		return nil, &ErrNotTranslatable{Reason: "vector search (MongoDB Atlas Vector Search is not wired up; use Qdrant or pgvector)"}
	}
	filter, err := mongoFilter(p.Where)
	if err != nil {
		return nil, err
	}
	opts := options.Find()
	if p.Limit > 0 {
		opts.SetLimit(int64(p.Limit))
	}
	if p.Offset > 0 {
		opts.SetSkip(int64(p.Offset))
	}
	if len(p.Orders) > 0 {
		sort := bson.D{}
		for _, o := range p.Orders {
			dir := 1
			if o.Desc {
				dir = -1
			}
			sort = append(sort, bson.E{Key: o.Column, Value: dir})
		}
		opts.SetSort(sort)
	}
	if len(p.Columns) > 0 {
		proj := bson.D{}
		for _, c := range p.Columns {
			proj = append(proj, bson.E{Key: c, Value: 1})
		}
		opts.SetProjection(proj)
	}
	cursor, err := s.coll(table).Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("orm: mongo find on %s: %w", table, err)
	}
	var docs []map[string]any
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("orm: mongo find on %s: %w", table, err)
	}
	return docs, nil
}

func (s *MongoStore) InsertDocs(ctx context.Context, table string, docs []map[string]any) error {
	if len(docs) == 0 {
		return nil
	}
	elems := make([]any, len(docs))
	for i, doc := range docs {
		elems[i] = mongoDoc(doc)
	}
	if _, err := s.coll(table).InsertMany(ctx, elems); err != nil {
		return fmt.Errorf("orm: mongo insert into %s: %w", table, err)
	}
	return nil
}

// UpsertDocs replaces each document wholesale: models carry complete
// documents, so matching the keys and replacing equals updating every
// column, which is what Upsert's default update list asks for.
func (s *MongoStore) UpsertDocs(ctx context.Context, table string, docs []map[string]any, keys []string) error {
	for _, doc := range docs {
		filter := make(bson.D, 0, len(keys))
		for _, k := range keys {
			filter = append(filter, bson.E{Key: k, Value: bsonValue(doc[k])})
		}
		_, err := s.coll(table).UpdateOne(ctx, filter,
			mongo.Pipeline{bson.D{{Key: "$replaceWith", Value: mongoDoc(doc)}}},
			options.UpdateOne().SetUpsert(true))
		if err != nil {
			return fmt.Errorf("orm: mongo upsert into %s: %w", table, err)
		}
	}
	return nil
}

func (s *MongoStore) UpdateDocs(ctx context.Context, table string, p Plan, sets map[string]any) (int64, error) {
	filter, err := mongoFilter(p.Where)
	if err != nil {
		return 0, err
	}
	set := bson.D{}
	for _, c := range sortedKeys(sets) {
		set = append(set, bson.E{Key: c, Value: bsonValue(sets[c])})
	}
	res, err := s.coll(table).UpdateMany(ctx, filter, bson.D{{Key: "$set", Value: set}})
	if err != nil {
		return 0, fmt.Errorf("orm: mongo update on %s: %w", table, err)
	}
	return res.MatchedCount, nil
}

func (s *MongoStore) DeleteDocs(ctx context.Context, table string, p Plan) (int64, error) {
	filter, err := mongoFilter(p.Where)
	if err != nil {
		return 0, err
	}
	res, err := s.coll(table).DeleteMany(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("orm: mongo delete on %s: %w", table, err)
	}
	return res.DeletedCount, nil
}

func (s *MongoStore) CountDocs(ctx context.Context, table string, p Plan) (int64, error) {
	filter, err := mongoFilter(p.Where)
	if err != nil {
		return 0, err
	}
	if p.Vector != nil {
		return 0, &ErrNotTranslatable{Reason: "vector search on MongoDB"}
	}
	n, err := s.coll(table).CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("orm: mongo count on %s: %w", table, err)
	}
	return n, nil
}

// mongoDoc converts a builder document to BSON: values implementing
// driver.Valuer (orm.JSON, orm.Vector, orm.Encrypted) go through their
// value form, times stay native.
func mongoDoc(doc map[string]any) bson.D {
	d := bson.D{}
	for _, k := range sortedKeys(doc) {
		d = append(d, bson.E{Key: k, Value: bsonValue(doc[k])})
	}
	return d
}

func bsonValue(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x
	case string, bool, int, int32, int64, uint, uint32, uint64, float64, nil:
		return v
	case []any, map[string]any:
		return v
	}
	if val, ok := v.(driver.Valuer); ok {
		if raw, err := val.Value(); err == nil && raw != nil {
			return raw
		}
	}
	return v
}

// mongoFilter compiles a plan condition to a BSON filter.
func mongoFilter(n Node) (bson.D, error) {
	if n == nil {
		return bson.D{}, nil
	}
	d, err := mongoCond(n)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func mongoCond(n Node) (bson.D, error) {
	switch c := n.(type) {
	case Field:
		v := mongoValue(c.Value)
		switch c.Op {
		case "=":
			return bson.D{{Key: c.Column, Value: v}}, nil
		case "<>":
			return bson.D{{Key: c.Column, Value: bson.D{{Key: "$ne", Value: v}}}}, nil
		case ">", ">=", "<", "<=":
			return bson.D{{Key: c.Column, Value: bson.D{{Key: "$" + mongoOp(c.Op), Value: v}}}}, nil
		case "LIKE", "NOT LIKE":
			re, err := likeRegex(c.Value)
			if err != nil {
				return nil, err
			}
			val := any(bson.D{{Key: "$regex", Value: re}})
			if c.Op == "NOT LIKE" {
				val = bson.D{{Key: "$not", Value: val}}
			}
			return bson.D{{Key: c.Column, Value: val}}, nil
		}
		return nil, &ErrNotTranslatable{Reason: "operator " + c.Op}
	case List:
		vals := make([]any, len(c.Values))
		for i, v := range c.Values {
			vals[i] = mongoValue(v)
		}
		op := "$in"
		if c.Not {
			op = "$nin"
		}
		return bson.D{{Key: c.Column, Value: bson.D{{Key: op, Value: vals}}}}, nil
	case Range:
		lo, hi := mongoValue(c.Lo), mongoValue(c.Hi)
		inner := bson.D{{Key: "$gte", Value: lo}, {Key: "$lte", Value: hi}}
		if c.Not {
			return bson.D{{Key: "$or", Value: bson.A{
				bson.D{{Key: c.Column, Value: bson.D{{Key: "$lt", Value: lo}}}},
				bson.D{{Key: c.Column, Value: bson.D{{Key: "$gt", Value: hi}}}},
			}}}, nil
		}
		return bson.D{{Key: c.Column, Value: inner}}, nil
	case NullTest:
		if c.Not {
			return bson.D{{Key: c.Column, Value: bson.D{{Key: "$ne", Value: nil}}}}, nil
		}
		return bson.D{{Key: c.Column, Value: nil}}, nil
	case Composite:
		parts := make(bson.A, 0, len(c.Parts))
		for _, p := range c.Parts {
			sub, err := mongoCond(p)
			if err != nil {
				return nil, err
			}
			if len(sub) == 0 {
				continue
			}
			parts = append(parts, sub)
		}
		if c.Or {
			return bson.D{{Key: "$or", Value: parts}}, nil
		}
		// AND: flatten sub-docs' keys; overlapping keys stay in $and.
		merged := bson.D{}
		conflict := false
		for _, sub := range parts {
			sd, _ := sub.(bson.D)
			for _, e := range sd {
				for _, m := range merged {
					if m.Key == e.Key {
						conflict = true
					}
				}
			}
			merged = append(merged, sd...)
		}
		if conflict {
			return bson.D{{Key: "$and", Value: parts}}, nil
		}
		return merged, nil
	case Negate:
		inner, err := mongoCond(c.Part)
		if err != nil {
			return nil, err
		}
		return bson.D{{Key: "$nor", Value: bson.A{inner}}}, nil
	case notTranslatable:
		return nil, &ErrNotTranslatable{Reason: "conditions on " + c.reason}
	}
	return nil, &ErrNotTranslatable{Reason: "this condition"}
}

func mongoOp(op string) string {
	return map[string]string{">": "gt", ">=": "gte", "<": "lt", "<=": "lte"}[op]
}

// likeRegex turns a SQL LIKE pattern (with % and _) into an anchored
// case-sensitive regular expression.
func likeRegex(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", &ErrNotTranslatable{Reason: "LIKE on a non-string"}
	}
	var b strings.Builder
	b.WriteString("^")
	for _, r := range s {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			if strings.ContainsRune(`\.+*?()[]{}|^$`, r) {
				b.WriteRune('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteString("$")
	return b.String(), nil
}

func mongoValue(v any) any {
	return bsonValue(v)
}
