# credit-report-service

Spring Boot service that exposes a REST API and persists data in PostgreSQL.

## Tech stack

- Java 17
- Spring Boot 3.3.x (Web, Data JPA, Validation, Actuator)
- PostgreSQL (JDBC + Flyway migrations)
- Lombok
- Maven
- Testcontainers for integration tests

## Project layout

```
src/main/java/com/example/creditreportservice/
├── CreditReportServiceApplication.java   # entry point
├── controller/                           # REST endpoints
├── service/                              # business logic
├── repository/                           # Spring Data JPA
├── model/                                # JPA entities
├── dto/                                  # request/response DTOs
├── exception/                            # custom exceptions + global handler
src/main/resources/
├── application.yml                       # default config
├── application-dev.yml                   # local dev profile
└── db/migration/                         # Flyway migrations
```

## Prerequisites

- JDK 17+
- Maven 3.8+ (or use the bundled wrapper — add via `mvn wrapper:wrapper`)
- PostgreSQL 13+ (or run one in Docker — see below)

## Running PostgreSQL locally

```bash
docker run -d --name credit-report-db \
  -e POSTGRES_DB=credit_report \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -p 5432:5432 \
  postgres:16
```

## Running the app

```bash
mvn spring-boot:run
# or with the dev profile
mvn spring-boot:run -Dspring-boot.run.profiles=dev
```

Configuration can be overridden via environment variables: `DB_URL`, `DB_USERNAME`,
`DB_PASSWORD`, `PORT`.

## Sample endpoints

| Method | Path                                  | Description                  |
|--------|---------------------------------------|------------------------------|
| GET    | `/api/ping`                           | Liveness probe               |
| GET    | `/api/credit-reports`                 | List all reports             |
| GET    | `/api/credit-reports/{id}`            | Get report by id             |
| GET    | `/api/credit-reports/by-subject/{id}` | Get report by subject        |
| POST   | `/api/credit-reports`                 | Create a report              |
| DELETE | `/api/credit-reports/{id}`            | Delete a report              |
| GET    | `/actuator/health`                    | Health check                 |

## Building & testing

```bash
mvn clean verify
```
