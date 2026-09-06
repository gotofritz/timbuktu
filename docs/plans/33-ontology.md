# Brief: Extend timbuktu with Ontology-Aware Retrieval

## Objective

Extend the existing RAG system, written in Go and using SQLite storage, with an **ontology + knowledge-graph layer** extracted from the existing document corpus.

The goal is not to replace the current RAG pipeline, but to add a structured semantic layer that improves:

* entity/concept understanding
* retrieval precision
* relationship-aware retrieval
* hierarchical reasoning
* explainability/provenance
* future corpus evolution

The architecture should remain pragmatic and suitable for a local/small-to-medium deployment.

---

## Before starting

How does this interact with topics (plan 32)? Do they clash? Would this supersed topics? In the interest of simplicity, if this can replace topics then I'm happy to remove that plan

## Core Concept

Separate three things:

1. **Ontology** — what kinds of things and relationships exist in the domain.
2. **Knowledge graph / instances** — concrete entities and facts extracted from documents.
3. **Document/vector retrieval** — the original unstructured evidence used to support answers.

Conceptually:

```text
                         DOCUMENTS
                             |
                             v
                    Entity / Relation
                       Extraction
                             |
                             v
                  KNOWLEDGE GRAPH
                  entities + triples
                             |
                             v
                  ONTOLOGY INDUCTION
                             |
                             v
                       ONTOLOGY
                 classes + relations
                             |
              +--------------+--------------+
              |                             |
              v                             v
       Graph/ontology retrieval       Vector retrieval
              |                             |
              +--------------+--------------+
                             |
                             v
                       LLM / RAG
                             |
                             v
                   Grounded answer
```

The ontology should be treated as a **structured semantic model**, not merely as text embedded into a vector database.

---

# 1. Ontology Model

The ontology should minimally represent:

### Classes / Concepts

Examples:

```text
Person
Organization
Software
Repository
ProgrammingLanguage
Framework
Issue
Feature
Dependency
```

### Relations / Properties

Examples:

```text
works_on(Person, Repository)
depends_on(Software, Software)
written_in(Software, ProgrammingLanguage)
uses(Software, Framework)
works_for(Person, Organization)
belongs_to(Issue, Repository)
affects(Issue, Software)
```

### Hierarchy

For example:

```text
DigitalAsset
├── Repository
└── Software

ComputeResource
├── VM
└── Container
```

Represent `is_a` / `subClassOf` explicitly.

### Attributes

Examples:

```text
Repository:
    name
    URL
    created_at

Software:
    name
    version
    license
```

### Optional constraints

Where useful, support:

```text
domain
range
inverse_of
transitive
symmetric
disjoint
```

Do not implement a full OWL/reasoning engine unless the existing application genuinely requires it. Prefer a small, explicit semantic model.

---

# 2. Distinguish Ontology from Knowledge Graph

This distinction is important.

Ontology:

```text
GoldenRetriever -> is_a -> Dog
Dog             -> is_a -> Animal
```

Knowledge graph instance:

```text
Fido -> is_a -> GoldenRetriever
```

The ontology defines the vocabulary and semantics.

The knowledge graph contains concrete entities/facts extracted from the corpus.

Documents remain the authoritative evidence for those facts.

---

# 3. Ontology Extraction / Induction Pipeline

Do not ask an LLM to directly generate a complete ontology from the entire corpus.

Use an iterative two-stage process.

## Stage A: Document-level extraction

For each document/chunk, extract candidate:

* entities/concepts
* entity mentions
* relationships/triples
* candidate types
* aliases
* evidence/provenance
* confidence

Example:

```json
{
  "entities": [
    {
      "text": "EC2 instance",
      "type_candidate": "compute resource"
    },
    {
      "text": "Auto Scaling Group",
      "type_candidate": "compute service"
    }
  ],
  "relations": [
    {
      "subject": "EC2 instance",
      "predicate": "member of",
      "object": "Auto Scaling Group"
    }
  ]
}
```

Store the source document/chunk for every extraction.

## Stage B: Ontology induction

Aggregate extraction results across the corpus.

For example:

```text
Candidate concepts:

EC2 instance          2,431 occurrences
Auto Scaling Group      817 occurrences
Launch Template         392 occurrences

Candidate relation:

EC2 instance
    -- member_of -->
Auto Scaling Group

Evidence: 183 occurrences
```

Then use an LLM and deterministic processing to propose:

* canonical classes
* canonical relationships
* aliases
* subclass relationships
* domain/range
* equivalent concepts
* potentially obsolete/duplicate concepts

The LLM should propose ontology changes from aggregated evidence rather than inventing them from scratch.

---

# 4. Entity and Relation Normalization

Documents will contain variants such as:

```text
EC2
EC2 instance
Amazon EC2 instance
instance
```

Implement an entity-resolution/normalization stage combining:

* exact/string matching
* aliases
* embeddings where useful
* LLM judgment for ambiguous cases

Likewise normalize predicates:

```text
uses
utilizes
makes use of
relies on
```

into a smaller canonical relation vocabulary where appropriate.

Avoid creating hundreds of semantically redundant relations.

---

# 5. Provenance

Every extracted fact and ontology statement should retain evidence.

Example:

```text
EC2 Instance
    -- member_of -->
Auto Scaling Group

confidence: 0.94

evidence:
    document_12 / chunk_7
    document_41 / chunk_3
    document_88 / chunk_12
```

This is important for:

* debugging
* explainability
* ontology review
* correcting bad extractions
* re-running induction
* tracing an answer back to source material

The document corpus should remain the ultimate source of truth.

---

# 6. SQLite Design

Extend the existing SQLite database rather than introducing a graph database unless there is a demonstrated need.

A possible logical schema:

```text
documents
chunks

entities
entity_mentions

relations
relation_instances

ontology_classes
ontology_properties
ontology_hierarchy

ontology_evidence
extraction_runs
```

Possible conceptual relationships:

```text
documents
   |
   +-- chunks
         |
         +-- entity_mentions --> entities
         |
         +-- relation evidence

entities
   |
   +-- entity types --> ontology_classes

relation_instances
   |
   +-- predicate --> ontology_properties
   |
   +-- subject --> entities
   |
   +-- object --> entities

ontology_classes
   |
   +-- hierarchy --> ontology_classes

ontology_*
   |
   +-- evidence --> documents/chunks
```

Use SQLite foreign keys and indexes appropriately.

The exact schema should be designed after inspecting the existing application's schema and query patterns.

---

# 7. RAG Retrieval Architecture

The ontology should complement, not replace, vector retrieval.

Given:

```text
User question
```

the retrieval pipeline should be capable of:

```text
Question
   |
   v
Entity / concept detection
   |
   +----------------------+
   |                      |
   v                      v
Ontology lookup       Vector search
   |                      |
   v                      v
Graph traversal       Relevant chunks
   |                      |
   +----------+-----------+
              |
              v
      Combined context
              |
              v
             LLM
```

Use ontology/graph retrieval for:

* exact relationships
* hierarchy
* entity expansion
* related concepts
* structured constraints

Use vector retrieval for:

* semantic similarity
* explanatory text
* facts not represented structurally
* supporting evidence

A hybrid retrieval strategy is preferred.

---

# 8. Graph Traversal

Implement lightweight graph traversal in SQLite.

For example, given:

```text
Golden Retriever
```

the retrieval system may discover:

```text
Golden Retriever
    -> is_a -> Dog
        -> is_a -> Animal
    -> has_property -> ...
```

The amount/depth of traversal should be configurable.

Avoid unrestricted graph expansion.

Potential configuration:

```text
max_hops = 1..3
max_entities = N
max_relationships = N
```

The retrieved graph context should be converted into compact structured context for the LLM.

---

# 9. Embeddings

Do **not** assume that embeddings alone accurately represent ontology semantics.

For example:

```text
Dog -> is_a -> Animal
```

and

```text
Animal -> is_a -> Dog
```

may be semantically close in embedding space despite being logically different.

Therefore:

* use embeddings for semantic retrieval
* store ontology relationships explicitly
* use graph traversal for exact semantics

If the existing RAG already stores embeddings in SQLite, investigate whether ontology/entity embeddings can reuse the same mechanism.

Do not introduce a separate vector database without a concrete reason.

---

# 10. Ontology Evolution

The system should support incremental updates.

When new documents arrive:

```text
New documents
     |
     v
Extraction
     |
     v
Existing ontology/entity matching
     |
     +--> known entities/facts
     |
     +--> new candidates
                |
                v
          ontology review
```

New concepts should initially be treated as **candidates**, rather than automatically becoming authoritative ontology classes.

This allows:

* confidence thresholds
* human review
* batch ontology updates
* detection of emerging concepts
* ontology versioning

Consider an `ontology_version` or extraction/induction run identifier.

---

# 11. Confidence and Lifecycle

Distinguish:

```text
candidate
approved
deprecated
rejected
```

for ontology concepts/relations.

Likewise retain extraction confidence.

Possible lifecycle:

```text
Document extraction
       |
       v
Candidate entity/relation
       |
       v
Normalization
       |
       v
Ontology proposal
       |
       +--> approved
       +--> rejected
       +--> needs review
```

Do not let low-confidence extraction silently modify the authoritative ontology.

---

# 12. Go Architecture

Before implementing anything, inspect the existing Go application and identify:

* document ingestion pipeline
* chunking
* embedding generation
* SQLite schema/repository layer
* vector search implementation
* current retrieval pipeline
* LLM abstraction
* configuration
* tests
* CLI/API boundaries

Prefer extending existing abstractions.

Potential logical components:

```text
OntologyExtractor
EntityResolver
RelationExtractor
OntologyInducer
OntologyRepository
KnowledgeGraphRepository
GraphRetriever
HybridRetriever
```

These are conceptual interfaces, not requirements for exact type names.

Keep LLM-specific functionality behind existing or new abstractions so the ontology system is not tightly coupled to one model/provider.

---

# 13. Suggested Interfaces

Consider interfaces along these lines:

```go
type OntologyRepository interface {
    GetClass(id int64) (...)
    FindClassByName(name string) (...)
    GetRelations(classID int64) (...)
    GetAncestors(classID int64, maxDepth int) (...)
}

type KnowledgeGraphRepository interface {
    FindEntity(name string) (...)
    GetRelations(entityID int64, maxHops int) (...)
    AddEntity(...)
    AddTriple(...)
}

type OntologyExtractor interface {
    Extract(ctx context.Context, chunk Chunk) (ExtractionResult, error)
}

type OntologyInducer interface {
    Induce(ctx context.Context, evidence []ExtractionResult) (OntologyProposal, error)
}
```

Adapt these to the existing codebase rather than forcing a new architecture.

---

# 14. Retrieval Output

The hybrid retriever should return both:

### Structured context

```text
Entity:
    Apache Spark

Type:
    Software

Relationships:
    Apache Spark -- written_in --> Scala
    Apache Spark -- uses --> Spark SQL
    Apache Spark -- depends_on --> ...
```

### Source evidence

```text
document_123 / chunk_4
document_456 / chunk_8
```

The LLM should receive enough provenance to ground claims in source documents.

---

# 15. Important Design Principles

### Keep ontology small

Do not attempt to create a perfect ontology of the entire corpus.

A small, high-confidence ontology is preferable to thousands of noisy classes.

### Keep facts separate from ontology

Ontology:

```text
Software -> written_in -> ProgrammingLanguage
```

Fact:

```text
Spark -> written_in -> Scala
```

### Preserve provenance

Every important extracted statement should be traceable to source text.

### Use hybrid retrieval

Ontology/graph retrieval and vector retrieval solve different problems.

### Prefer deterministic structure where possible

Do not rely on embeddings or LLMs for relationships whose semantics need to be exact.

### Make LLM extraction replaceable

The architecture should allow different models/providers.

### Design for incremental evolution

The ontology should improve as more documents are processed.

### Don't over-engineer initially

SQLite + Go + existing vector RAG should remain the foundation.

A graph database, RDF store, OWL reasoner, or external vector database should only be introduced if actual requirements justify it.

---

# 16. Expected Deliverable from Codex

First **inspect the existing repository and architecture**.

Do not immediately implement.

Produce an architecture proposal covering:

1. Existing RAG architecture relevant to this feature
2. Proposed ontology/knowledge-graph architecture
3. SQLite schema changes
4. Go package/module changes
5. Extraction pipeline
6. Ontology induction pipeline
7. Entity/relation normalization
8. Hybrid retrieval strategy
9. Provenance model
10. Incremental update strategy
11. Configuration
12. Testing strategy
13. Migration/backward compatibility
14. Example end-to-end query flow
15. Trade-offs and alternatives

Then propose an implementation plan in incremental stages.

The existing RAG should continue to work if ontology extraction is disabled.

The preferred result is an **incremental extension of the current system**, not a rewrite.
