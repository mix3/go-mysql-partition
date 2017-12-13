package partition

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"
)

const (
	PartitionTypeList  = "LIST"
	PartitionTypeRange = "RANGE"

	CatchAllPartitionValue = "MAXVALUE"
)

// Partition is XXX
type Partition struct {
	Name        string
	Description string
	Comment     string
}

// Partitioner is XXX
type Partitioner interface {
	IsPartitioned() (bool, error)
	HasPartition(Partition) (bool, error)

	Creates(...Partition) error
	Adds(...Partition) error
	Drops(...Partition) error
	Truncates(...Partition) error

	PrepareCreates(...Partition) (Handler, error)
	PrepareAdds(...Partition) (Handler, error)
	PrepareDrops(...Partition) (Handler, error)
	PrepareTruncates(...Partition) (Handler, error)

	Dryrun(bool)
}

// Handler is XXX
type Handler interface {
	Execute() error
	Statement() string
}

type partBuilder interface {
	buildPart(Partition) (string, error)
}

type partitioner struct {
	table         string
	db            *sql.DB
	partitionType string
	expression    string
	partBuilder   partBuilder

	dryrun  bool
	verbose bool

	// lazy load
	_partitions []string
	_dbName     string
}

func (p *partitioner) dbName() (string, error) {
	if p._dbName != "" {
		return p._dbName, nil
	}

	if err := p.db.QueryRow("SELECT DATABASE()").Scan(&p._dbName); err != nil {
		return "", errors.Wrap(err, "error select database name.")
	}

	return p._dbName, nil
}

func (p *partitioner) retrievePartitions() ([]string, error) {
	dbName, err := p.dbName()
	if err != nil {
		return nil, errors.Wrap(err, "error get db name.")
	}

	stmt, err := p.db.Prepare(`
SELECT
  partition_name
FROM
  information_schema.PARTITIONS
WHERE
  table_name		= ? AND
  table_schema		= ? AND
  partition_method	= ?
ORDER BY
  partition_name
`)
	if err != nil {
		return nil, errors.Wrap(err, "error prepare.")
	}

	rows, err := stmt.Query(p.table, dbName, p.partitionType)
	if err != nil {
		return nil, errors.Wrap(err, "error select partitions.")
	}

	partitions := []string{}
	for rows.Next() {
		var part string
		if err := rows.Scan(&part); err != nil {
			if err != sql.ErrNoRows {
				return nil, errors.Wrap(err, "error scan partition.")
			}
			return partitions, nil
		}
		partitions = append(partitions, part)
	}

	return partitions, nil
}

func (p *partitioner) IsPartitioned() (bool, error) {
	parts, err := p.retrievePartitions()
	if err != nil {
		return false, errors.Wrap(err, "error retrieve partitons")
	}

	if 0 < len(parts) {
		return true, nil
	}

	return false, nil
}

func (p *partitioner) HasPartition(partition Partition) (bool, error) {
	parts, err := p.retrievePartitions()
	if err != nil {
		return false, errors.Wrap(err, "error retrieve paritions")
	}

	for _, part := range parts {
		if part == partition.Name {
			return true, nil
		}
	}

	return false, nil
}

func (p *partitioner) buildParts(partitions ...Partition) (string, error) {
	parts := []string{}
	for _, partition := range partitions {
		part, err := p.partBuilder.buildPart(partition)
		if err != nil {
			return "", errors.Wrapf(err, "error build part. name:%s descriptions:%s", partition.Name, partition.Description)
		}
		parts = append(parts, part)
	}

	return strings.Join(parts, ", "), nil
}

func (p *partitioner) buildCreatesSQL(partitions ...Partition) (string, error) {
	if r, ok := p.partBuilder.(*Range); ok && r.catchAllPartitionName != "" {
		partitions = append(partitions, Partition{Name: r.catchAllPartitionName, Description: CatchAllPartitionValue})
	}

	parts, err := p.buildParts(partitions...)
	if err != nil {
		return "", errors.Wrap(err, "error build parts")
	}

	return fmt.Sprintf("ALTER TABLE %s PARTITION BY %s (%s) (%s)", p.table, p.partitionType, p.expression, parts), nil
}

func (p *partitioner) buildAddsSQL(partitions ...Partition) (string, error) {
	parts, err := p.buildParts(partitions...)
	if err != nil {
		return "", errors.Wrap(err, "error build parts")
	}

	return fmt.Sprintf("ALTER TABLE %s ADD PARTITION (%s)", p.table, parts), nil
}

func (p *partitioner) buildDropsSQL(partitions ...Partition) (string, error) {
	names := []string{}
	for _, partition := range partitions {
		names = append(names, partition.Name)
	}

	return fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", p.table, strings.Join(names, ",")), nil
}

func (p *partitioner) buildTruncatesSQL(partitions ...Partition) (string, error) {
	names := []string{}
	for _, partition := range partitions {
		names = append(names, partition.Name)
	}

	return fmt.Sprintf("ALTER TABLE %s TRUNCATE PARTITION %s", p.table, strings.Join(names, ",")), nil
}

func (p *partitioner) Creates(partitions ...Partition) error {
	h, err := p.PrepareCreates(partitions...)
	if err != nil {
		return errors.Wrap(err, "error prepare adds")
	}
	return h.Execute()
}

func (p *partitioner) Adds(partitions ...Partition) error {
	h, err := p.PrepareAdds(partitions...)
	if err != nil {
		return errors.Wrap(err, "error prepare adds")
	}
	return h.Execute()
}

func (p *partitioner) Drops(partitions ...Partition) error {
	h, err := p.PrepareDrops(partitions...)
	if err != nil {
		return errors.Wrap(err, "error prepare adds")
	}
	return h.Execute()
}

func (p *partitioner) Truncates(partitions ...Partition) error {
	h, err := p.PrepareTruncates(partitions...)
	if err != nil {
		return errors.Wrap(err, "error prepare adds")
	}
	return h.Execute()
}

func (p *partitioner) PrepareCreates(partitions ...Partition) (Handler, error) {
	stmt, err := p.buildCreatesSQL(partitions...)
	if err != nil {
		return nil, errors.Wrap(err, "error build sql")
	}
	return &handler{
		statement:   stmt,
		partitioner: p,
	}, nil
}

func (p *partitioner) PrepareAdds(partitions ...Partition) (Handler, error) {
	stmt, err := p.buildAddsSQL(partitions...)
	if err != nil {
		return nil, errors.Wrap(err, "error build sql")
	}
	return &handler{
		statement:   stmt,
		partitioner: p,
	}, nil
}

func (p *partitioner) PrepareDrops(partitions ...Partition) (Handler, error) {
	stmt, err := p.buildDropsSQL(partitions...)
	if err != nil {
		return nil, errors.Wrap(err, "error build sql")
	}
	return &handler{
		statement:   stmt,
		partitioner: p,
	}, nil
}

func (p *partitioner) PrepareTruncates(partitions ...Partition) (Handler, error) {
	stmt, err := p.buildTruncatesSQL(partitions...)
	if err != nil {
		return nil, errors.Wrap(err, "error build sql")
	}
	return &handler{
		statement:   stmt,
		partitioner: p,
	}, nil
}

func (p *partitioner) Dryrun(dryrun bool) {
	p.dryrun = dryrun
}

type Option func(*partitioner)

func Dryrun(dryrun bool) Option {
	return func(p *partitioner) {
		p.dryrun = dryrun
	}
}

func Verbose(verbose bool) Option {
	return func(p *partitioner) {
		p.verbose = verbose
	}
}

func PartitionType(t string) Option {
	return func(p *partitioner) {
		p.partitionType = strings.ToUpper(t)
	}
}

func CatchAllPartitionName(name string) Option {
	return func(p *partitioner) {
		if r, ok := p.partBuilder.(*Range); ok {
			r.catchAllPartitionName = name
		}
	}
}

type handler struct {
	statement   string
	executed    bool
	partitioner *partitioner
}

func (h *handler) Execute() error {
	if h.executed {
		return errors.New("error statement is already execute")
	}

	if h.partitioner.verbose || h.partitioner.dryrun {
		prefix := ""
		if h.partitioner.dryrun {
			prefix = " (dry-run)"
		}

		fmt.Printf("Following SQL sttement to be executed%s.\n", prefix)
		fmt.Println(h.statement)
	}

	if !h.partitioner.dryrun {
		if _, err := h.partitioner.db.Exec(h.statement); err != nil {
			return errors.Wrap(err, "error exec statement")
		}

		if h.partitioner.verbose {
			fmt.Println("done.")
		}
	}
	h.executed = true

	return nil
}

func (h *handler) Statement() string {
	return h.statement
}
