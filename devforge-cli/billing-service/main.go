package main

import (
"context"
"database/sql"
"log"
"time"
)

func main() {
 db, err := sql.Open("postgres", "user:password@localhost:5432/billingdb")
 if err != nil {
 log.Fatal(err)
 }
 defer db.Close()

 stmt, err := db.Prepare("SELECT * FROM invoices")
 if err != nil {
 log.Fatal(err)
 }
 defer stmt.Close()

 rows, err := stmt.Query()
 if err != nil {
 log.Fatal(err)
 }

 for rows.Next() {
 var invoice struct {
 ID int
 Name string
 DueDate time.Time
 }
 err := rows.Scan(&invoice.ID, &invoice.Name, &invoice.DueDate)
 if err != nil {
 log.Fatal(err)
 }
 log.Println(invoice)
 }
}
