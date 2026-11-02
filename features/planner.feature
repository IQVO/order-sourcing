Feature: The planner's REST surface
  The network-ops planner lists and inspects assignments and asks for an immediate
  re-optimisation of a QUEUED one (apis/openapi.yaml). Errors are RFC 9457 problem
  documents with the shared slugs; the Idempotency-Key of the action is required.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"

  Scenario: Listing shows the queued groups
    When a GET request is sent to "/assignments"
    Then the response status is 200
    And the response list "items" has 1 item
    And the response list "items" contains an item whose "orderId" is "ord-1"
    And the response list "items" contains an item whose "state" is "QUEUED"
    And the response list "items" contains an item whose "candidate.nodeId" is "fc-a"

  Scenario: Filters combine and an empty result is still 200
    When a GET request is sent to "/assignments?state=RELEASED"
    Then the response status is 200
    And the response list "items" has 0 items
    And the response field "items" is "[]"

  Scenario: The nodeId filter matches the current candidate node
    When a GET request is sent to "/assignments?nodeId=fc-a"
    Then the response status is 200
    And the response list "items" has 1 item
    When a GET request is sent to "/assignments?nodeId=fc-z&orderId=ord-1"
    Then the response status is 200
    And the response list "items" has 0 items

  Scenario: An unknown state filter is a validation problem
    When a GET request is sent to "/assignments?state=BOGUS"
    Then the response status is 422
    And the response problem type is "validation-failed"

  Scenario Outline: A limit outside 1..500 is an invalid-limit problem
    When a GET request is sent to "/assignments?limit=<limit>"
    Then the response status is 400
    And the response problem type is "invalid-limit"

    Examples:
      | limit |
      | abc   |
      | 0     |
      | 501   |

  Scenario: Getting one assignment returns its full shape
    When a GET request is sent to the assignment of "ord-1" group 1
    Then the response status is 200
    And the response field "orderId" is "ord-1"
    And the response field "groupNo" is "1"
    And the response field "state" is "QUEUED"
    And the response field "promise.tier" is "NEXT_DAY"
    And the response field "promise.revisionNo" is "0"
    And the response field "candidate.nodeId" is "fc-a"
    And the response list "releases" has 0 items
    And the response has no field "lockedAt"

  Scenario: An unknown assignment id is not found
    When a GET request is sent to "/assignments/asg-nope"
    Then the response status is 404
    And the response problem type is "not-found"

  Scenario: Re-optimising over REST returns the assignment on its new node
    Given the node "fc-a" has no stock of "cat-1" any more
    When a reoptimise request is posted for the assignment of "ord-1" group 1
    Then the response status is 200
    And the response field "state" is "QUEUED"
    And the response field "candidate.nodeId" is "fc-b"

  Scenario: A repeat on unchanged read models answers with the same candidate
    When a reoptimise request is posted for the assignment of "ord-1" group 1
    Then the response status is 200
    And the response field "candidate.nodeId" is "fc-a"

  Scenario: A stale version over REST is a conflict
    When a reoptimise request is posted for the assignment of "ord-1" group 1 with the JSON body {"assignmentVersion":99}
    Then the response status is 409
    And the response problem type is "stale-assignment-version"

  Scenario: An assignment pending its node's answer is a conflict
    Given I release the assignment of "ord-1" group 1
    When a reoptimise request is posted for the assignment of "ord-1" group 1
    Then the response status is 409
    And the response problem type is "release-in-flight"

  Scenario: A locked assignment cannot be re-optimised over REST
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When a reoptimise request is posted for the assignment of "ord-1" group 1
    Then the response status is 409
    And the response problem type is "assignment-locked"

  Scenario: Re-optimising an unknown assignment is not found
    When a POST request is sent to "/assignments/asg-nope/reoptimisations"
    Then the response status is 404
    And the response problem type is "not-found"

  Scenario Outline: A body the operation cannot take is rejected before the use case
    When a reoptimise request is posted for the assignment of "ord-1" group 1 with the JSON body <body>
    Then the response status is <status>
    And the response problem type is "<slug>"

    Examples:
      | body                    | status | slug              |
      | {                       | 400    | malformed-request |
      | {} {}                   | 400    | malformed-request |
      | {"nope":1}              | 400    | malformed-request |
      | {"assignmentVersion":"x"} | 400  | malformed-request |
      | {"assignmentVersion":-1}  | 422  | validation-failed |

  Scenario: The action needs an Idempotency-Key
    When a reoptimise request for the assignment of "ord-1" group 1 is sent without an Idempotency-Key
    Then the response status is 400
    And the response problem type is "idempotency-key-required"

  Scenario: An Idempotency-Key longer than 128 characters is an invalid key
    When a reoptimise request for the assignment of "ord-1" group 1 is sent with a 129-character Idempotency-Key
    Then the response status is 400
    And the response problem type is "idempotency-key-invalid"

  Scenario: A retired paging parameter is rejected loudly
    When a GET request is sent to "/assignments?after=asg-1"
    Then the response status is 400
    And the response problem type is "unknown-query-parameter"
